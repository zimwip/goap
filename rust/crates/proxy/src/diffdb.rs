//! `goap-proxy diff-db`: the tables of the two databases of a comparison, compared row by row once the Rust ids are
//! read back in Go ids and the times masked. The ids are the pairs of the comparison (`ids.json` of the report), then
//! the ones of the rows alike once their ids are left out, one of a kind on each side (an id no answer showed: the
//! bootstrap's, a link's), table after table until no new pair comes.
//!
//! ```text
//! goap-proxy diff-db --go <dsn> --rust <dsn> [--ids <ids.json>] [--tables a,b] [--skip a,b]
//! ```
//! A DSN is `postgres://…` or the path of a SQLite file. Rows are compared as multisets (no order).
use std::collections::{BTreeMap, HashMap};

use anyhow::{Context, bail};
use serde_json::{Map, Value};

use crate::ids::{IdMap, Toward};

/// The tables left out by default: the bookkeeping of the migrations and the outboxes of delivered events.
const SKIP: &[&str] = &["schema_migrations", "event_outbox", "domain_outbox"];

struct Args {
    go: String,
    rust: String,
    ids: Option<String>,
    tables: Vec<String>,
    skip: Vec<String>,
}

fn parse(args: &[String]) -> anyhow::Result<Args> {
    let mut a = Args {
        go: String::new(),
        rust: String::new(),
        ids: None,
        tables: vec![],
        skip: SKIP.iter().map(|s| s.to_string()).collect(),
    };
    let mut it = args.iter();
    while let Some(k) = it.next() {
        let mut v = || it.next().cloned().with_context(|| format!("{k} needs a value"));
        match k.as_str() {
            "--go" => a.go = v()?,
            "--rust" => a.rust = v()?,
            "--ids" => a.ids = Some(v()?),
            "--tables" => a.tables = v()?.split(',').map(String::from).collect(),
            "--skip" => a.skip.extend(v()?.split(',').map(String::from)),
            _ => bail!("diff-db: unknown argument {k:?}"),
        }
    }
    if a.go.is_empty() || a.rust.is_empty() {
        bail!("diff-db: --go and --rust are required");
    }
    Ok(a)
}

/// The rows of every table of a database, each a JSON object.
async fn dump(dsn: &str) -> anyhow::Result<BTreeMap<String, Vec<Value>>> {
    if dsn.starts_with("postgres://") || dsn.starts_with("postgresql://") {
        let (client, conn) = tokio_postgres::connect(dsn, tokio_postgres::NoTls)
            .await
            .with_context(|| format!("connecting to {dsn}"))?;
        tokio::spawn(conn);
        let tables = client
            .query(
                "SELECT table_name::text FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'",
                &[],
            )
            .await?;
        let mut out = BTreeMap::new();
        for t in tables {
            let name: String = t.get(0);
            let rows = client
                .query(&format!("SELECT row_to_json(t)::text FROM \"{name}\" t"), &[])
                .await?;
            let rows = rows
                .iter()
                .map(|r| serde_json::from_str(r.get::<_, &str>(0)))
                .collect::<Result<Vec<Value>, _>>()?;
            out.insert(name, rows);
        }
        Ok(out)
    } else {
        let path = dsn.trim_start_matches("sqlite://").trim_start_matches("file:");
        let path = path.split('?').next().unwrap_or(path).to_string();
        tokio::task::spawn_blocking(move || dump_sqlite(&path)).await?
    }
}

fn dump_sqlite(path: &str) -> anyhow::Result<BTreeMap<String, Vec<Value>>> {
    use rusqlite::types::ValueRef;
    let db = rusqlite::Connection::open_with_flags(path, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)
        .with_context(|| format!("opening {path}"))?;
    let names: Vec<String> = db
        .prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")?
        .query_map([], |r| r.get(0))?
        .collect::<Result<_, _>>()?;
    let mut out = BTreeMap::new();
    for name in names {
        let mut st = db.prepare(&format!("SELECT * FROM \"{name}\""))?;
        let cols: Vec<String> = st.column_names().into_iter().map(String::from).collect();
        let rows = st
            .query_map([], |r| {
                let mut o = Map::new();
                for (i, c) in cols.iter().enumerate() {
                    let v = match r.get_ref(i)? {
                        ValueRef::Null => Value::Null,
                        ValueRef::Integer(n) => Value::from(n),
                        ValueRef::Real(f) => Value::from(f),
                        ValueRef::Text(t) => Value::String(String::from_utf8_lossy(t).into_owned()),
                        ValueRef::Blob(b) => Value::String(format!(
                            "\\x{}",
                            b.iter().map(|x| format!("{x:02x}")).collect::<String>()
                        )),
                    };
                    o.insert(c.clone(), v);
                }
                Ok(Value::Object(o))
            })?
            .collect::<Result<Vec<_>, _>>()?;
        out.insert(name, rows);
    }
    Ok(out)
}

/// A row as compared: ids in Go ids, times masked, a JSON text column read as JSON.
fn canonical(v: &Value, ids: &IdMap, toward: Option<Toward>) -> String {
    fn walk(v: &Value, ids: &IdMap, toward: Option<Toward>) -> Value {
        match v {
            Value::String(s) => {
                let s = match toward {
                    Some(t) => ids.text(s, t).into_owned(),
                    None => s.clone(),
                };
                if (s.starts_with('{') || s.starts_with('['))
                    && let Ok(j) = serde_json::from_str::<Value>(&s)
                {
                    return walk(&j, ids, None);
                }
                crate::compare::normalise(&Value::String(s), "", &[])
            }
            Value::Array(a) => Value::Array(a.iter().map(|x| walk(x, ids, toward)).collect()),
            Value::Object(o) => Value::Object(o.iter().map(|(k, x)| (k.clone(), walk(x, ids, toward))).collect()),
            other => other.clone(),
        }
    }
    walk(v, ids, toward).to_string()
}

/// Pairs the ids of the rows that are one of a kind on each side once their unknown ids are blanked, until no new
/// pair comes (a pair makes other rows distinct).
fn pair_rows(ids: &mut IdMap, go: &BTreeMap<String, Vec<Value>>, rust: &BTreeMap<String, Vec<Value>>) {
    // a JSON text column read as JSON, so that its ids pair like the others
    fn parsed(v: &Value) -> Value {
        match v {
            Value::String(s) if s.starts_with('{') || s.starts_with('[') => {
                serde_json::from_str(s).unwrap_or_else(|_| v.clone())
            }
            Value::Object(o) => Value::Object(o.iter().map(|(k, x)| (k.clone(), parsed(x))).collect()),
            other => other.clone(),
        }
    }
    let go: BTreeMap<&String, Vec<Value>> = go
        .iter()
        .map(|(k, rows)| (k, rows.iter().map(parsed).collect()))
        .collect();
    let rust: BTreeMap<&String, Vec<Value>> = rust
        .iter()
        .map(|(k, rows)| (k, rows.iter().map(parsed).collect()))
        .collect();
    for _round in 0..32 {
        let n = ids.len();
        for (table, g) in &go {
            let Some(r) = rust.get(table) else { continue };
            let mut groups: HashMap<String, (Vec<usize>, Vec<usize>)> = HashMap::new();
            for (i, row) in g.iter().enumerate() {
                groups
                    .entry(ids.masked(row, Toward::Go).to_string())
                    .or_default()
                    .0
                    .push(i);
            }
            for (j, row) in r.iter().enumerate() {
                groups
                    .entry(ids.masked(row, Toward::Rust).to_string())
                    .or_default()
                    .1
                    .push(j);
            }
            for (gi, rj) in groups.values() {
                if let ([i], [j]) = (gi.as_slice(), rj.as_slice()) {
                    let _ = ids.pair_json(&g[*i], &r[*j]);
                }
            }
        }
        if ids.len() == n {
            break;
        }
    }
}

pub async fn run(args: &[String]) -> anyhow::Result<()> {
    let a = parse(args)?;
    let ids = match &a.ids {
        Some(p) => {
            let pairs: HashMap<String, String> =
                serde_json::from_slice(&std::fs::read(p).with_context(|| format!("reading {p}"))?)?;
            IdMap::from_pairs(pairs)
        }
        None => IdMap::default(),
    };
    let (go, rust) = tokio::try_join!(dump(&a.go), dump(&a.rust))?;
    let mut ids = ids;
    let before = ids.len();
    pair_rows(&mut ids, &go, &rust);
    println!(
        "ids: {before} paired by the comparison, {} by the rows",
        ids.len() - before
    );
    let mut names: Vec<&String> = go.keys().chain(rust.keys()).collect();
    names.sort();
    names.dedup();
    let mut divergent = 0usize;
    let empty = Vec::new();
    for name in names {
        if a.skip.contains(name) || (!a.tables.is_empty() && !a.tables.contains(name)) {
            continue;
        }
        let g = go.get(name).unwrap_or(&empty);
        let r = rust.get(name).unwrap_or(&empty);
        // ids in a JSON column are translated too: the walk translates every string before reading it as JSON
        let mut count: HashMap<String, i64> = HashMap::new();
        for row in g {
            *count.entry(canonical(row, &ids, None)).or_default() += 1;
        }
        for row in r {
            *count.entry(canonical(row, &ids, Some(Toward::Go))).or_default() -= 1;
        }
        let only_go: Vec<&String> = count.iter().filter(|(_, n)| **n > 0).map(|(k, _)| k).collect();
        let only_rust: Vec<&String> = count.iter().filter(|(_, n)| **n < 0).map(|(k, _)| k).collect();
        let status = if !go.contains_key(name) {
            "only in rust"
        } else if !rust.contains_key(name) {
            "only in go"
        } else if only_go.is_empty() && only_rust.is_empty() {
            "same"
        } else {
            "differs"
        };
        println!("{name:<32} go {:>8}  rust {:>8}  {status}", g.len(), r.len());
        if status != "same" {
            divergent += 1;
            println!(
                "    {} row(s) only in go, {} only in rust",
                only_go.len(),
                only_rust.len()
            );
            // GOAP_DIFF_FULL=1 prints every row whole
            let full = std::env::var_os("GOAP_DIFF_FULL").is_some();
            let (n, show): (usize, fn(&str) -> String) = if full { (usize::MAX, str::to_string) } else { (3, short) };
            for k in only_go.iter().take(n) {
                println!("    - go   {}", show(k));
            }
            for k in only_rust.iter().take(n) {
                println!("    + rust {}", show(k));
            }
        }
    }
    if divergent > 0 {
        bail!("{divergent} table(s) differ");
    }
    println!("every table is the same");
    Ok(())
}

fn short(s: &str) -> String {
    if s.len() <= 400 {
        return s.to_string();
    }
    let mut end = 400;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    format!("{}…", &s[..end])
}
