//! Checks that the PostgreSQL and the SQLite migrations of a service describe the same schema (ADR 0074). Both
//! dialects are kept by hand; this guard reads the migrations of each (CREATE TABLE, CREATE INDEX, ALTER TABLE ...
//! ADD FOREIGN KEY / RENAME, DROP TABLE, in file order), reduces them to a schema in a common vocabulary and reports
//! every difference in tables, columns, nullability, defaults, keys, constraints and indexes. It is test-only: only
//! dev-dependencies name it.
//!
//! What the two dialects spell differently and is not a difference of schema:
//!
//! - types: uuid, timestamptz, jsonb, bigserial / bigint / boolean, integer[] are stored in SQLite as text or
//!   integers (uuid, timestamptz, jsonb, integer[] -> text; boolean, bigint, bigserial -> integer);
//! - a boolean default (false / true) is 0 / 1, the empty integer array default '{}' is the JSON '[]';
//! - a serial column and an AUTOINCREMENT primary key are "generated"; the generation of a column that is not the
//!   primary key is not compared (SQLite only generates a rowid key: the repository assigns such a column);
//! - DEFAULT now() has no SQLite counterpart (the repositories write the timestamp themselves): it is ignored;
//! - a boolean in an index predicate (WHERE NOT superseded) is the test of its integer (superseded = 0);
//! - a foreign key declared inline or by ALTER TABLE is the same constraint.
use std::{
    collections::{BTreeMap, BTreeSet},
    fmt,
    path::Path,
    sync::LazyLock,
};

use regex::Regex;

/// The result of a service's migrations.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Schema {
    pub tables: BTreeMap<String, Table>,
    pub indexes: BTreeMap<String, Index>,
}

/// A table in the common vocabulary.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Table {
    pub name: String,
    pub columns: Vec<Column>,
    pub pk: Vec<String>,
    pub unique: Vec<Vec<String>>,
    /// normalised: `(cols) -> table(cols) [deferred] [on delete x]`
    pub fks: Vec<String>,
    pub checks: Vec<String>,
}

/// A column in the common vocabulary.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Column {
    pub name: String,
    /// `text` | `integer`
    pub r#type: String,
    /// the type as written
    pub raw: String,
    pub not_null: bool,
    pub default: String,
    pub generated: bool,
}

/// An index in the common vocabulary.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Index {
    pub name: String,
    pub table: String,
    pub cols: String,
    pub unique: bool,
    pub r#where: String,
}

/// A migration this package does not understand, or that cannot be read.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error(pub String);

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Error {}

fn err(s: impl Into<String>) -> Error {
    Error(s.into())
}

macro_rules! re {
    ($name:ident, $re:expr) => {
        static $name: LazyLock<Regex> = LazyLock::new(|| Regex::new($re).expect($re));
    };
}

/// Reads the migrations of `dir` (`*.sql` directly in it, in name order) under `root` on disk.
pub fn load(root: &Path, dir: &str) -> Result<Schema, Error> {
    let path = root.join(dir);
    let mut files = BTreeMap::new();
    let entries = std::fs::read_dir(&path).map_err(|e| err(format!("{}: {e}", path.display())))?;
    for e in entries {
        let e = e.map_err(|e| err(e.to_string()))?;
        let name = e.file_name().to_string_lossy().to_string();
        if name.ends_with(".sql") && e.path().is_file() {
            let content = std::fs::read_to_string(e.path()).map_err(|e| err(e.to_string()))?;
            files.insert(format!("{dir}/{name}"), content);
        }
    }
    load_files(&files, dir)
}

/// Reads the migrations of `dir` (`<dir>/*.sql`, in name order) out of `files` (path to content).
pub fn load_files(files: &BTreeMap<String, String>, dir: &str) -> Result<Schema, Error> {
    let prefix = format!("{dir}/");
    let names: Vec<&String> = files
        .keys()
        .filter(|n| {
            n.strip_prefix(&prefix)
                .is_some_and(|rest| !rest.contains('/') && rest.ends_with(".sql"))
        })
        .collect();
    if names.is_empty() {
        return Err(err(format!("no migration in {dir}")));
    }
    let mut s = Schema::default();
    for n in names {
        for stmt in statements(&files[n]) {
            s.apply(&stmt)
                .map_err(|e| err(format!("{n}: {e}\n  {stmt}")))?;
        }
    }
    for t in s.tables.values_mut() {
        t.finish();
    }
    let tables = s.tables.clone();
    for ix in s.indexes.values_mut() {
        if let Some(t) = tables.get(&ix.table) {
            ix.r#where = t.bool_predicate(&ix.r#where);
        }
    }
    Ok(s)
}

re!(LINE_COMMENT, r"--[^\n]*");
re!(SPACES, r"\s+");

/// Splits a migration into its statements (no semicolon occurs inside a string of ours).
fn statements(sql: &str) -> Vec<String> {
    let sql = LINE_COMMENT.replace_all(sql, "");
    sql.split(';')
        .map(|s| SPACES.replace_all(s, " ").trim().to_string())
        .filter(|s| !s.is_empty())
        .collect()
}

re!(
    RE_CREATE_TABLE,
    r"(?is)^CREATE TABLE (?:IF NOT EXISTS )?(\w+) \((.*)\)$"
);
re!(
    RE_CREATE_INDEX,
    r"(?is)^CREATE (UNIQUE )?INDEX (?:IF NOT EXISTS )?(\w+) ON (\w+) ?\(([^)]*)\)(?: WHERE (.*))?$"
);
re!(RE_DROP_TABLE, r"(?is)^DROP TABLE (?:IF EXISTS )?(\w+)$");
re!(RE_DROP_INDEX, r"(?is)^DROP INDEX (?:IF EXISTS )?(\w+)$");
re!(RE_RENAME, r"(?is)^ALTER TABLE (\w+) RENAME TO (\w+)$");
re!(RE_ADD_FK, r"(?is)^ALTER TABLE (\w+) ADD (FOREIGN KEY .*)$");
re!(
    RE_ADD_COLUMN,
    r"(?is)^ALTER TABLE (\w+) ADD (?:COLUMN )?(\w+ .*)$"
);
re!(
    RE_DROP_COLUMN,
    r"(?is)^ALTER TABLE (\w+) DROP (?:COLUMN )?(\w+)$"
);

fn group<'a>(c: &regex::Captures<'a>, i: usize) -> &'a str {
    c.get(i).map_or("", |m| m.as_str())
}

impl Schema {
    fn apply(&mut self, stmt: &str) -> Result<(), Error> {
        let up = stmt.to_uppercase();
        if up.starts_with("INSERT ") || up.starts_with("UPDATE ") || up.starts_with("DELETE ") {
            return Ok(()); // data, not schema
        }
        if let Some(m) = RE_CREATE_TABLE.captures(stmt) {
            let t = parse_table(group(&m, 1), group(&m, 2))?;
            self.tables.insert(group(&m, 1).to_string(), t);
            return Ok(());
        }
        if let Some(m) = RE_CREATE_INDEX.captures(stmt) {
            let name = group(&m, 2).to_string();
            self.indexes.insert(
                name.clone(),
                Index {
                    name,
                    table: group(&m, 3).to_string(),
                    cols: group(&m, 4).trim().replace(" ,", ","),
                    unique: !group(&m, 1).is_empty(),
                    r#where: group(&m, 5).trim().to_string(),
                },
            );
            return Ok(());
        }
        if let Some(m) = RE_DROP_TABLE.captures(stmt) {
            let t = group(&m, 1);
            self.tables.remove(t);
            self.indexes.retain(|_, ix| ix.table != t);
            return Ok(());
        }
        if let Some(m) = RE_DROP_INDEX.captures(stmt) {
            self.indexes.remove(group(&m, 1));
            return Ok(());
        }
        if let Some(m) = RE_RENAME.captures(stmt) {
            let (from, to) = (group(&m, 1), group(&m, 2));
            let mut t = self
                .tables
                .remove(from)
                .ok_or_else(|| err(format!("rename of an unknown table {from}")))?;
            t.name = to.to_string();
            self.tables.insert(to.to_string(), t);
            for ix in self.indexes.values_mut() {
                if ix.table == from {
                    ix.table = to.to_string();
                }
            }
            return Ok(());
        }
        if let Some(m) = RE_ADD_FK.captures(stmt) {
            let t = self.table_mut(group(&m, 1))?;
            return t.add_constraint(group(&m, 2));
        }
        if let Some(m) = RE_DROP_COLUMN.captures(stmt) {
            let (tn, cn) = (group(&m, 1), group(&m, 2));
            let t = self.table_mut(tn)?;
            let i = t
                .columns
                .iter()
                .position(|c| c.name == cn)
                .ok_or_else(|| err(format!("unknown column {tn}.{cn}")))?;
            t.columns.remove(i);
            return Ok(());
        }
        if let Some(m) = RE_ADD_COLUMN.captures(stmt) {
            let t = self.table_mut(group(&m, 1))?;
            return t.add_item(group(&m, 2));
        }
        Err(err("statement not understood by sqlschematest"))
    }

    fn table_mut(&mut self, name: &str) -> Result<&mut Table, Error> {
        self.tables
            .get_mut(name)
            .ok_or_else(|| err(format!("ALTER of an unknown table {name}")))
    }
}

/// Splits `s` at the commas outside parentheses.
fn split_top(s: &str) -> Vec<String> {
    let mut out = Vec::new();
    let (mut depth, mut start) = (0i32, 0usize);
    for (i, r) in s.char_indices() {
        match r {
            '(' => depth += 1,
            ')' => depth -= 1,
            ',' if depth == 0 => {
                out.push(s[start..i].trim().to_string());
                start = i + 1;
            }
            _ => {}
        }
    }
    out.push(s[start..].trim().to_string());
    out
}

fn parse_table(name: &str, body: &str) -> Result<Table, Error> {
    let mut t = Table {
        name: name.to_string(),
        ..Default::default()
    };
    for item in split_top(body) {
        t.add_item(&item)
            .map_err(|e| err(format!("table {name}: {e}")))?;
    }
    Ok(t)
}

re!(
    CONSTRAINT_START,
    r"(?i)^(PRIMARY KEY|UNIQUE|FOREIGN KEY|CHECK|CONSTRAINT)\b"
);
re!(RE_TABLE_PK, r"(?is)^PRIMARY KEY ?\(([^)]*)\)$");
re!(RE_TABLE_UNIQUE, r"(?is)^UNIQUE ?\(([^)]*)\)$");
re!(
    RE_TABLE_FK,
    r"(?is)^FOREIGN KEY ?\(([^)]*)\) REFERENCES (\w+) ?\(([^)]*)\)(.*)$"
);
re!(RE_TABLE_CHECK, r"(?is)^CHECK ?\((.*)\)$");

fn cols(s: &str) -> Vec<String> {
    s.split(',').map(|c| c.trim().to_string()).collect()
}

fn fk_text(from: &[String], table: &str, to: &[String], rest: &str) -> String {
    let r = rest.trim().to_uppercase();
    let mut s = format!("({}) -> {}({})", from.join(", "), table, to.join(", "));
    if r.contains("DEFERRABLE INITIALLY DEFERRED") {
        s.push_str(" deferred");
    }
    if r.contains("ON DELETE CASCADE") {
        s.push_str(" on delete cascade");
    }
    s
}

re!(RE_COLUMN, r"(?is)^(\w+) +([A-Za-z]+(?:\[\])?)(.*)$");
re!(RE_REF_INLINE, r"(?is)REFERENCES (\w+) ?\((\w+)\)(.*)$");
re!(
    RE_DEFAULT,
    r"(?is)DEFAULT +('(?:[^']|'')*'|\w+\(\)|[-\w.]+)"
);
re!(RE_CHECK_INL, r"(?is)CHECK ?\((.*)\)");
re!(RE_STRIP_BLOCK, r"(?is)(REFERENCES.*|CHECK ?\(.*\))");

impl Table {
    fn add_item(&mut self, item: &str) -> Result<(), Error> {
        if CONSTRAINT_START.is_match(item) {
            self.add_constraint(item)
        } else {
            self.add_column(item)
        }
    }

    fn add_constraint(&mut self, item: &str) -> Result<(), Error> {
        if let Some(m) = RE_TABLE_PK.captures(item) {
            self.pk = cols(group(&m, 1));
        } else if let Some(m) = RE_TABLE_UNIQUE.captures(item) {
            self.unique.push(cols(group(&m, 1)));
        } else if let Some(m) = RE_TABLE_FK.captures(item) {
            self.fks.push(fk_text(
                &cols(group(&m, 1)),
                group(&m, 2),
                &cols(group(&m, 3)),
                group(&m, 4),
            ));
        } else if let Some(m) = RE_TABLE_CHECK.captures(item) {
            self.checks.push(norm_expr(group(&m, 1)));
        } else {
            return Err(err(format!("constraint not understood: {item}")));
        }
        Ok(())
    }

    fn add_column(&mut self, item: &str) -> Result<(), Error> {
        let m = RE_COLUMN
            .captures(item)
            .ok_or_else(|| err(format!("column not understood: {item}")))?;
        let mut c = Column {
            name: group(&m, 1).to_string(),
            raw: group(&m, 2).to_string(),
            ..Default::default()
        };
        let rest = group(&m, 3);
        let up = rest.to_uppercase();
        if matches!(c.raw.to_lowercase().as_str(), "bigserial" | "serial") {
            c.generated = true;
            c.not_null = true;
        }
        c.r#type = base_type(&c.raw).to_string();
        if up.contains("NOT NULL") {
            c.not_null = true;
        }
        if up.contains("AUTOINCREMENT") {
            c.generated = true;
        }
        if up.contains("PRIMARY KEY") {
            self.pk = vec![c.name.clone()];
            c.not_null = true;
        }
        if up.contains(" UNIQUE") {
            self.unique.push(vec![c.name.clone()]);
        }
        if let Some(mm) = RE_REF_INLINE.captures(rest) {
            self.fks.push(fk_text(
                std::slice::from_ref(&c.name),
                group(&mm, 1),
                &[group(&mm, 2).to_string()],
                group(&mm, 3),
            ));
        }
        if let Some(mm) = RE_CHECK_INL.captures(rest) {
            self.checks.push(norm_expr(group(&mm, 1)));
        }
        // the DEFAULT of the column proper, not one inside a CHECK or a REFERENCES clause
        let stripped = RE_STRIP_BLOCK.replace_all(rest, "");
        if let Some(mm) = RE_DEFAULT.captures(&stripped) {
            c.default = norm_default(&c.raw, group(&mm, 1));
        }
        self.columns.push(c);
        Ok(())
    }

    /// Sorts what is a set.
    fn finish(&mut self) {
        self.fks.sort();
        self.checks.sort();
        self.unique.sort_by_key(|u| u.join(","));
    }

    /// Rewrites a PostgreSQL boolean test of the table's columns in an index predicate as the SQLite one.
    fn bool_predicate(&self, r#where: &str) -> String {
        if r#where.is_empty() {
            return String::new();
        }
        let mut w = r#where.to_string();
        for c in &self.columns {
            if !c.raw.eq_ignore_ascii_case("boolean") && !c.raw.eq_ignore_ascii_case("bool") {
                continue;
            }
            let name = regex::escape(&c.name);
            let not = Regex::new(&format!(r"(?i)\bNOT {name}\b")).expect("column regex");
            w = not
                .replace_all(&w, format!("{} = 0", c.name).as_str())
                .to_string();
            let alone = Regex::new(&format!(r"(?i)^{name}$")).expect("column regex");
            w = alone
                .replace_all(&w, format!("{} = 1", c.name).as_str())
                .to_string();
        }
        norm_expr(&w)
    }
}

fn base_type(raw: &str) -> &'static str {
    match raw.to_lowercase().as_str() {
        "boolean" | "bool" | "integer" | "int" | "bigint" | "bigserial" | "serial" | "smallint" => {
            "integer"
        }
        _ => "text", // text, uuid, timestamptz, jsonb, integer[] ...
    }
}

fn norm_expr(s: &str) -> String {
    SPACES.replace_all(s, " ").trim().to_string()
}

/// Reduces a default to the SQLite vocabulary; `""` is no default.
fn norm_default(raw: &str, d: &str) -> String {
    let d = d.trim();
    let low = d.to_lowercase();
    if low == "now()" || low == "current_timestamp" {
        return String::new(); // SQLite has no counterpart, the repositories write the time
    }
    if low == "false" {
        return "0".into();
    }
    if low == "true" {
        return "1".into();
    }
    if raw.eq_ignore_ascii_case("integer[]") && d == "'{}'" {
        return "'[]'".into();
    }
    d.to_string()
}

/// Lists the differences between the schemas of the two dialects, as readable lines (none: aligned).
pub fn diff(pg: &Schema, lite: &Schema) -> Vec<String> {
    let mut out = Vec::new();
    for name in union(pg.tables.keys(), lite.tables.keys()) {
        let (p, l) = match (pg.tables.get(&name), lite.tables.get(&name)) {
            (None, _) => {
                out.push(format!("table {name}: only in SQLite"));
                continue;
            }
            (_, None) => {
                out.push(format!("table {name}: only in PostgreSQL"));
                continue;
            }
            (Some(p), Some(l)) => (p, l),
        };
        let (pc, lc) = (columns_by_name(p), columns_by_name(l));
        for cn in union(pc.keys(), lc.keys()) {
            match (pc.get(cn.as_str()), lc.get(cn.as_str())) {
                (None, _) => out.push(format!("column {name}.{cn}: only in SQLite")),
                (_, None) => out.push(format!("column {name}.{cn}: only in PostgreSQL")),
                (Some(a), Some(b)) => {
                    if a.r#type != b.r#type {
                        out.push(format!(
                            "column {name}.{cn}: type {} ({}) / {} ({})",
                            a.r#type, a.raw, b.r#type, b.raw
                        ));
                    }
                    if a.not_null != b.not_null {
                        out.push(format!(
                            "column {name}.{cn}: NOT NULL is {} in PostgreSQL, {} in SQLite",
                            a.not_null, b.not_null
                        ));
                    }
                    if a.default != b.default {
                        out.push(format!(
                            "column {name}.{cn}: default {:?} in PostgreSQL, {:?} in SQLite",
                            a.default, b.default
                        ));
                    }
                    if a.generated != b.generated && p.pk.contains(&cn) {
                        out.push(format!(
                            "column {name}.{cn}: generated {} in PostgreSQL, {} in SQLite",
                            a.generated, b.generated
                        ));
                    }
                }
            }
        }
        if order(p) != order(l) {
            out.push(format!(
                "table {name}: column order {} (PostgreSQL) / {} (SQLite)",
                order(p),
                order(l)
            ));
        }
        let (a, b) = (p.pk.join(","), l.pk.join(","));
        if a != b {
            out.push(format!(
                "table {name}: primary key ({a}) in PostgreSQL, ({b}) in SQLite"
            ));
        }
        diff_set(
            &mut out,
            &format!("table {name}: unique"),
            &unique_texts(p),
            &unique_texts(l),
        );
        diff_set(
            &mut out,
            &format!("table {name}: foreign key"),
            &p.fks,
            &l.fks,
        );
        diff_set(
            &mut out,
            &format!("table {name}: check"),
            &p.checks,
            &l.checks,
        );
    }
    for name in union(pg.indexes.keys(), lite.indexes.keys()) {
        match (pg.indexes.get(&name), lite.indexes.get(&name)) {
            (None, _) => out.push(format!("index {name}: only in SQLite")),
            (_, None) => out.push(format!("index {name}: only in PostgreSQL")),
            (Some(p), Some(l)) if p != l => out.push(format!(
                "index {name}: {} ({}) {} where {:?} in PostgreSQL, {} ({}) {} where {:?} in SQLite",
                p.table,
                p.cols,
                uniq(p.unique),
                p.r#where,
                l.table,
                l.cols,
                uniq(l.unique),
                l.r#where
            )),
            _ => {}
        }
    }
    out
}

fn uniq(b: bool) -> &'static str {
    if b { "unique" } else { "non-unique" }
}

fn order(t: &Table) -> String {
    t.columns
        .iter()
        .map(|c| c.name.as_str())
        .collect::<Vec<_>>()
        .join(",")
}

fn unique_texts(t: &Table) -> Vec<String> {
    t.unique
        .iter()
        .map(|u| format!("({})", u.join(", ")))
        .collect()
}

fn diff_set(out: &mut Vec<String>, what: &str, pg: &[String], lite: &[String]) {
    for s in pg {
        if !lite.contains(s) {
            out.push(format!("{what} {s}: only in PostgreSQL"));
        }
    }
    for s in lite {
        if !pg.contains(s) {
            out.push(format!("{what} {s}: only in SQLite"));
        }
    }
}

fn columns_by_name(t: &Table) -> BTreeMap<&str, &Column> {
    t.columns.iter().map(|c| (c.name.as_str(), c)).collect()
}

fn union<S: AsRef<str>>(
    a: impl IntoIterator<Item = S>,
    b: impl IntoIterator<Item = S>,
) -> Vec<String> {
    a.into_iter()
        .chain(b)
        .map(|s| s.as_ref().to_string())
        .collect::<BTreeSet<_>>()
        .into_iter()
        .collect()
}

/// Panics (fails the test) when the migrations of `pg_dir` and `lite_dir` (relative to the current directory: the
/// crate under test) describe different schemas.
pub fn assert_aligned(pg_dir: &str, lite_dir: &str) {
    let root = Path::new(".");
    let pg = load(root, pg_dir).unwrap_or_else(|e| panic!("PostgreSQL migrations: {e}"));
    let lite = load(root, lite_dir).unwrap_or_else(|e| panic!("SQLite migrations: {e}"));
    let d = diff(&pg, &lite);
    assert!(
        d.is_empty(),
        "the PostgreSQL ({pg_dir}) and SQLite ({lite_dir}) schemas diverge:\n  {}",
        d.join("\n  ")
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    fn load_map(files: &[(&str, &str)], dir: &str) -> Schema {
        let m: BTreeMap<String, String> = files
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        load_files(&m, dir).unwrap()
    }

    /// Equivalent spellings of one schema are aligned.
    #[test]
    fn aligned_spellings() {
        let pg = load_map(
            &[(
                "pg/0001.sql",
                "
CREATE TABLE a (id uuid PRIMARY KEY, n integer[] NOT NULL DEFAULT '{}', ok boolean NOT NULL DEFAULT false, at timestamptz NOT NULL DEFAULT now(), seq bigserial);
CREATE TABLE b (id uuid PRIMARY KEY, a_id uuid NOT NULL REFERENCES a(id) DEFERRABLE INITIALLY DEFERRED, dead boolean NOT NULL DEFAULT false);
ALTER TABLE a ADD FOREIGN KEY (id) REFERENCES b(id) DEFERRABLE INITIALLY DEFERRED;
CREATE UNIQUE INDEX b_live ON b (a_id) WHERE NOT dead;",
            )],
            "pg",
        );
        let lite = load_map(
            &[(
                "s/0001.sql",
                "
CREATE TABLE a (id text PRIMARY KEY, n text NOT NULL DEFAULT '[]', ok integer NOT NULL DEFAULT 0, at text NOT NULL, seq INTEGER NOT NULL, FOREIGN KEY (id) REFERENCES b(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TABLE b (id text PRIMARY KEY, a_id text NOT NULL REFERENCES a(id) DEFERRABLE INITIALLY DEFERRED, dead integer NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX b_live ON b (a_id) WHERE dead = 0;",
            )],
            "s",
        );
        let d = diff(&pg, &lite);
        assert!(d.is_empty(), "spellings should align: {d:?}");
    }

    /// Every kind of divergence is reported.
    #[test]
    fn divergences() {
        let pg = load_map(
            &[(
                "pg/0001.sql",
                "
CREATE TABLE a (id uuid PRIMARY KEY, x text NOT NULL, y text, z text NOT NULL DEFAULT '', only_pg text);
CREATE INDEX a_x ON a (x);
CREATE TABLE gone (id text);",
            )],
            "pg",
        );
        let lite = load_map(
            &[(
                "s/0001.sql",
                "
CREATE TABLE a (id text PRIMARY KEY, x text, y integer, z text NOT NULL DEFAULT 'q');
CREATE INDEX a_x ON a (x, y);
CREATE TABLE extra (id text);",
            )],
            "s",
        );
        let got = diff(&pg, &lite).join("\n");
        for want in [
            "table extra: only in SQLite",
            "table gone: only in PostgreSQL",
            "column a.only_pg: only in PostgreSQL",
            "column a.x: NOT NULL",
            "column a.y: type",
            r#"column a.z: default "''" in PostgreSQL, "'q'" in SQLite"#,
            "index a_x:",
        ] {
            assert!(got.contains(want), "missing {want:?} in:\n{got}");
        }
    }

    /// A later migration (rename, drop) is applied before the comparison.
    #[test]
    fn migrations_apply_in_order() {
        let s = load_map(
            &[
                (
                    "d/0001.sql",
                    "CREATE TABLE t (id text PRIMARY KEY); CREATE TABLE old (k text); INSERT INTO t VALUES ('x');",
                ),
                ("d/0002.sql", "DROP TABLE old; ALTER TABLE t RENAME TO t2;"),
            ],
            "d",
        );
        assert!(
            s.tables.contains_key("t2") && s.tables.len() == 1,
            "tables = {:?}",
            s.tables.keys().collect::<Vec<_>>()
        );
    }

    #[test]
    fn no_migration_and_not_understood() {
        assert!(load_files(&BTreeMap::new(), "x").is_err());
        let m: BTreeMap<String, String> =
            [("x/1.sql".to_string(), "GRANT ALL ON t TO u".to_string())].into();
        let e = load_files(&m, "x").unwrap_err();
        assert!(e.0.contains("statement not understood"), "{e}");
    }
}
