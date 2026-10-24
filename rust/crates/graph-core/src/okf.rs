//! The state of a graph at a baseline as an OKF tree (`graph/okf`, ADR 0111): one Markdown file per node, its
//! identity, properties and links in a YAML front matter, its long texts and its links in the body, and an index of
//! the namespace, in a zip archive.

use crate::codetext;
use crate::goyaml::{self, Yaml};
use crate::snapshot::{Snapshot, SnapshotNode, SnapshotProvenance};
use chrono::{DateTime, Datelike, FixedOffset, Timelike, Utc};
use graph_catalog::Catalog;
use serde_json::Value as Json;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::io::Write;

/// The OKF layout written here, recorded in the front matter of the index.
pub const FORMAT: &str = "goap.okf/v1";
const INDEX_FILE: &str = "index.md";
const KEY_LINK_PROPERTIES: &str = "linkProperties";
const KEY_PROVENANCE: &str = "provenance";
const PROPERTY_PREFIX: &str = "prop_";

/// An attribute rendered in the body rather than in the front matter.
struct BodyAttribute {
    name: String,
    label: String,
    code: bool,
}

/// How the nodes of a type are rendered (`okf.FromTypes`): the catalogue's `asName` attribute names them, its text
/// areas and code attributes are the body; without a catalogue or for a type it does not know, the defaults.
pub struct Options<'a> {
    pub catalog: Option<&'a Catalog>,
}

impl Options<'_> {
    fn name(&self, typ: &str) -> Option<String> {
        let t = self.catalog?.typ(typ)?;
        t.attributes.iter().find(|a| a.def.as_name).map(|a| a.def.name.clone())
    }

    fn body(&self, typ: &str) -> Vec<BodyAttribute> {
        let Some(t) = self.catalog.and_then(|c| c.typ(typ)) else {
            return vec![BodyAttribute {
                name: "description".into(),
                label: "Description".into(),
                code: false,
            }];
        };
        t.attributes
            .iter()
            .filter_map(|a| {
                let d = &a.def;
                let code = d.typ == "code";
                (code || d.widget == "textarea" && (d.typ.is_empty() || d.typ == "string")).then(|| BodyAttribute {
                    name: d.name.clone(),
                    label: if d.label.is_empty() {
                        d.name.clone()
                    } else {
                        d.label.clone()
                    },
                    code,
                })
            })
            .collect()
    }
}

struct Entry<'s> {
    node: &'s SnapshotNode,
    /// relative to the folder of the namespace
    path: String,
    title: String,
}

/// Renders a snapshot as an OKF tree into a zip archive (`okf.Write`); `progress` is told how many nodes are
/// written. An error is Go's: a node whose front matter does not read back.
pub fn write(
    s: &Snapshot,
    o: &Options,
    at: DateTime<FixedOffset>,
    mut progress: impl FnMut(usize, usize),
) -> Result<Vec<u8>, String> {
    let root = safe_name(&s.namespace);
    let mut entries: Vec<Entry> = Vec::with_capacity(s.nodes.len());
    let mut taken = HashSet::new();
    for n in &s.nodes {
        let dir = safe_name(type_name(&n.typ));
        let base = safe_name(&n.key);
        let mut p = format!("{dir}/{base}.md");
        let mut i = 2;
        while taken.contains(&p.to_lowercase()) {
            p = format!("{dir}/{base}-{i}.md");
            i += 1;
        }
        taken.insert(p.to_lowercase());
        entries.push(Entry {
            node: n,
            path: p,
            title: title_of(n, o),
        });
    }
    let mut by_key: HashMap<&str, usize> = HashMap::new();
    for (i, e) in entries.iter().enumerate() {
        by_key.insert(&e.node.key, i);
    }
    let mut incoming: HashMap<&str, Vec<(&str, usize)>> = HashMap::new();
    for (i, e) in entries.iter().enumerate() {
        for l in &e.node.links {
            incoming.entry(&l.to).or_default().push((&l.typ, i));
        }
    }
    let mut z = Zip::new(at);
    z.put(&format!("{root}/{INDEX_FILE}"), render_index(s, &entries).as_bytes());
    for (i, e) in entries.iter().enumerate() {
        progress(i, entries.len());
        let mut inc = incoming.remove(e.node.key.as_str()).unwrap_or_default();
        let content =
            render_node(s, e, &entries, &by_key, &mut inc, o).map_err(|err| format!("node {}: {err}", e.node.key))?;
        z.put(&format!("{root}/{}", e.path), content.as_bytes());
    }
    Ok(z.finish())
}

fn render_node(
    s: &Snapshot,
    e: &Entry,
    entries: &[Entry],
    by_key: &HashMap<&str, usize>,
    incoming: &mut [(&str, usize)],
    o: &Options,
) -> Result<String, String> {
    let n = e.node;
    let props = n.props.as_ref();
    let prop_str = |k: &str| props.and_then(|p| p.get(k)).and_then(Json::as_str);
    let body = o.body(&n.typ);
    let in_body: HashSet<&str> = body
        .iter()
        .filter(|a| prop_str(&a.name).is_some())
        .map(|a| a.name.as_str())
        .collect();
    let fm = front_matter(s, e, entries, by_key, &in_body)?;
    let mut b = front_matter_text(&fm);
    b.push_str(&format!("# {}\n", one_line(&e.title)));
    for a in &body {
        if !in_body.contains(a.name.as_str()) {
            continue;
        }
        let mut text = prop_str(&a.name).unwrap_or("").trim().to_string();
        if text.is_empty() {
            continue;
        }
        if a.code
            && let Some(v) = codetext::parse(&text)
        {
            text = codetext::block(&v).trim().to_string();
        }
        b.push_str(&format!("\n## {}\n\n{text}\n", one_line(&a.label)));
    }
    if !n.links.is_empty() {
        b.push_str("\n## Links\n\n");
        for l in &n.links {
            let t = by_key.get(l.to.as_str()).map(|&i| &entries[i]);
            let title = t.map_or(String::new(), |t| one_line(&t.title));
            b.push_str(&format!("- {}: [{title}]({})\n", type_name(&l.typ), rel_path(e, t)));
        }
    }
    if !incoming.is_empty() {
        incoming.sort_by(|a, b| {
            a.0.cmp(b.0)
                .then_with(|| entries[a.1].node.key.cmp(&entries[b.1].node.key))
        });
        b.push_str("\n## Referenced by\n\n");
        for (typ, from) in incoming.iter() {
            let f = &entries[*from];
            b.push_str(&format!(
                "- [{}]({}) {}\n",
                one_line(&f.title),
                rel_path(e, Some(f)),
                type_name(typ)
            ));
        }
    }
    Ok(b)
}

/// The flat front matter of a node (`okf.frontMatter`), each value as `yaml.Node.Encode` holds it.
fn front_matter(
    s: &Snapshot,
    e: &Entry,
    entries: &[Entry],
    by_key: &HashMap<&str, usize>,
    in_body: &HashSet<&str>,
) -> Result<Vec<(String, Yaml)>, String> {
    let n = e.node;
    let mut m: Vec<(String, Yaml)> = Vec::new();
    let has = |m: &[(String, Yaml)], k: &str| m.iter().any(|(x, _)| x == k);
    let put = |m: &mut Vec<(String, Yaml)>, k: &str, v: Yaml| -> Result<(), String> {
        m.push((k.to_string(), goyaml::round_trip(v)?));
        Ok(())
    };
    put(&mut m, "key", Yaml::str(&n.key))?;
    put(&mut m, "type", Yaml::str(&n.typ))?;
    put(&mut m, "title", Yaml::str(&e.title))?;
    if !n.state.is_empty() {
        put(&mut m, "state", Yaml::str(&n.state))?;
    }
    if !n.owner.is_empty() {
        put(&mut m, "owner", Yaml::str(&n.owner))?;
    }
    put(&mut m, "namespace", Yaml::str(&s.namespace))?;
    if !s.baseline.is_empty() {
        put(&mut m, "baseline", Yaml::str(&s.baseline))?;
    }
    put(&mut m, "aliases", Yaml::Seq(vec![Yaml::str(&n.key)]))?;
    let mut tags = vec![Yaml::str(format!(
        "{}/{}",
        tag_of(&s.namespace),
        tag_of(type_name(&n.typ))
    ))];
    if !n.state.is_empty() {
        tags.push(Yaml::str(format!("state/{}", tag_of(&n.state))));
    }
    put(&mut m, "tags", Yaml::Seq(tags))?;
    let mut by_type: BTreeMap<&str, Vec<Yaml>> = BTreeMap::new();
    let mut types: Vec<&str> = Vec::new();
    let mut link_props = Vec::new();
    for l in &n.links {
        let t = type_name(&l.typ);
        if !by_type.contains_key(t) {
            types.push(t);
        }
        let target = by_key.get(l.to.as_str()).map(|&i| &entries[i]);
        by_type.entry(t).or_default().push(Yaml::str(wikilink(target)));
        if let Some(p) = l.props.as_ref().filter(|p| !p.is_empty()) {
            link_props.push(Yaml::Map(vec![
                (Yaml::str("type"), Yaml::str(&l.typ)),
                (Yaml::str("to"), Yaml::str(&l.to)),
                (Yaml::str("properties"), Yaml::of_json(&Json::Object(p.clone()))),
            ]));
        }
    }
    for t in types {
        if !has(&m, t) {
            let v = by_type.remove(t).unwrap_or_default();
            put(&mut m, t, Yaml::Seq(v))?;
        }
    }
    if let Some(p) = &n.provenance {
        put(&mut m, KEY_PROVENANCE, provenance(p))?;
    }
    if let Some(props) = &n.props {
        for (k, v) in props {
            if in_body.contains(k.as_str()) {
                continue;
            }
            // the same string already there (the title)
            let same = m
                .iter()
                .any(|(x, y)| x == k && matches!((y, v), (Yaml::Str(a), Json::String(b)) if a == b));
            if same {
                continue;
            }
            let name =
                if has(&m, k) || k == KEY_LINK_PROPERTIES || k == KEY_PROVENANCE || k.starts_with(PROPERTY_PREFIX) {
                    format!("{PROPERTY_PREFIX}{k}")
                } else {
                    k.clone()
                };
            put(&mut m, &name, Yaml::of_json(v))?;
        }
    }
    if !link_props.is_empty() {
        put(&mut m, KEY_LINK_PROPERTIES, Yaml::Seq(link_props))?;
    }
    Ok(m)
}

fn provenance(p: &SnapshotProvenance) -> Yaml {
    let mut m = vec![(Yaml::str("version"), Yaml::int(p.version))];
    if !p.change.is_empty() {
        m.push((Yaml::str("change"), Yaml::str(&p.change)));
    }
    if !p.produced_by.is_empty() {
        m.push((Yaml::str("producedBy"), Yaml::str(&p.produced_by)));
    }
    if p.model {
        m.push((Yaml::str("model"), Yaml::Plain("true".into())));
    }
    Yaml::Map(m)
}

fn front_matter_text(m: &[(String, Yaml)]) -> String {
    let doc = Yaml::Map(m.iter().map(|(k, v)| (Yaml::NodeKey(k.clone()), v.clone())).collect());
    format!("---\n{}---\n\n", goyaml::to_string(&doc))
}

fn render_index(s: &Snapshot, entries: &[Entry]) -> String {
    let mut by_type: BTreeMap<&str, Vec<&Entry>> = BTreeMap::new();
    for e in entries {
        by_type.entry(&e.node.typ).or_default().push(e);
    }
    let mut idx = vec![
        (Yaml::str("format"), Yaml::str(FORMAT)),
        (Yaml::str("namespace"), Yaml::str(&s.namespace)),
    ];
    if !s.branch.is_empty() {
        idx.push((Yaml::str("branch"), Yaml::str(&s.branch)));
    }
    if !s.baseline.is_empty() {
        idx.push((Yaml::str("baseline"), Yaml::str(&s.baseline)));
    }
    idx.push((
        Yaml::str("exportedAt"),
        Yaml::Plain(crate::gojson::time_text(&s.exported_at.with_timezone(&Utc))),
    ));
    idx.push((Yaml::str("nodes"), Yaml::int(entries.len() as i64)));
    if !by_type.is_empty() {
        let mut types: Vec<(&str, usize)> = by_type.iter().map(|(t, es)| (*t, es.len())).collect();
        types.sort_by(|a, b| goyaml::key_order(a.0, b.0));
        idx.push((
            Yaml::str("types"),
            Yaml::Map(
                types
                    .into_iter()
                    .map(|(t, n)| (Yaml::str(t), Yaml::int(n as i64)))
                    .collect(),
            ),
        ));
    }
    let mut b = format!("---\n{}---\n\n", goyaml::to_string(&Yaml::Map(idx)));
    b.push_str(&format!("# {}\n", one_line(&s.namespace)));
    if s.baseline.is_empty() {
        b.push_str("\nNothing committed yet: the empty state.\n");
    } else {
        b.push_str(&format!(
            "\nState of baseline `{}` (branch {}).\n",
            s.baseline, s.branch
        ));
    }
    for (t, es) in &mut by_type {
        b.push_str(&format!("\n## {t}\n\n"));
        es.sort_by(|a, b| a.node.key.cmp(&b.node.key));
        for e in es.iter() {
            if e.title != e.node.key {
                b.push_str(&format!("- [{}]({}) `{}`\n", one_line(&e.title), e.path, e.node.key));
            } else {
                b.push_str(&format!("- [{}]({})\n", one_line(&e.title), e.path));
            }
        }
    }
    b
}

/// An Obsidian link to the file of a node: its path without extension, its title as label.
fn wikilink(t: Option<&Entry>) -> String {
    let Some(t) = t else {
        return "[[|]]".into();
    };
    let label = one_line(&t.title).replace('|', "-").replace(']', ")").replace('[', "(");
    format!("[[{}|{label}]]", t.path.strip_suffix(".md").unwrap_or(&t.path))
}

/// A value made a valid tag: letters, digits, `_`, `-` and `/` kept, anything else `-`.
fn tag_of(s: &str) -> String {
    s.chars()
        .map(|r| {
            if r.is_ascii_alphanumeric() || matches!(r, '_' | '-' | '/') {
                r
            } else {
                '-'
            }
        })
        .collect()
}

fn title_of(n: &SnapshotNode, o: &Options) -> String {
    let names = match o.name(&n.typ) {
        Some(a) if !a.is_empty() => vec![a],
        _ => vec!["title".to_string(), "name".to_string()],
    };
    for a in names {
        if let Some(v) = n.props.as_ref().and_then(|p| p.get(&a)).and_then(Json::as_str)
            && !v.trim().is_empty()
        {
            return v.trim().to_string();
        }
    }
    n.key.clone()
}

/// The path of the file of `to` relative to the file of `from` (both one folder below the root).
fn rel_path(from: &Entry, to: Option<&Entry>) -> String {
    let Some(to) = to else {
        return String::new();
    };
    let dir = |p: &str| p.rsplit_once('/').map_or(".", |(d, _)| d).to_string();
    if dir(&from.path) == dir(&to.path) {
        return to.path.rsplit_once('/').map_or(to.path.clone(), |(_, b)| b.to_string());
    }
    format!("../{}", to.path)
}

/// The local name of a qualified type (`alm@Requirement`: `Requirement`), as `domain.ParseTypeRef` reads it.
fn type_name(typ: &str) -> &str {
    match typ.split_once('@') {
        None => typ,
        Some((ns, name)) if !ns.is_empty() && !name.is_empty() && !name.contains('@') => name,
        Some(_) => typ,
    }
}

/// A key made a portable file name: letters, digits, `.`, `_` and `-` kept, anything else `-`; never empty nor a dot
/// name.
fn safe_name(s: &str) -> String {
    let out: String = s
        .chars()
        .map(|r| {
            if r.is_ascii_alphanumeric() || matches!(r, '_' | '-' | '.') {
                r
            } else {
                '-'
            }
        })
        .collect();
    let out = out.trim_matches('.');
    if out.is_empty() { "_".into() } else { out.into() }
}

/// A title kept on one line of Markdown.
fn one_line(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// A zip archive written in memory, its files deflated.
struct Zip {
    out: Vec<u8>,
    central: Vec<u8>,
    count: u16,
    time: u16,
    date: u16,
}

impl Zip {
    fn new(at: DateTime<FixedOffset>) -> Zip {
        let t = at.naive_local();
        let time = ((t.hour() << 11) | (t.minute() << 5) | (t.second() / 2)) as u16;
        let date = (((t.year().max(1980) - 1980) as u32) << 9 | (t.month() << 5) | t.day()) as u16;
        Zip {
            out: Vec::new(),
            central: Vec::new(),
            count: 0,
            time,
            date,
        }
    }

    fn put(&mut self, name: &str, content: &[u8]) {
        let mut enc = flate2::write::DeflateEncoder::new(Vec::new(), flate2::Compression::new(5));
        let _ = enc.write_all(content);
        let data = enc.finish().unwrap_or_default();
        let mut crc = flate2::Crc::new();
        crc.update(content);
        let offset = self.out.len() as u32;
        let flags: u16 = if name.is_ascii() { 0 } else { 0x800 };
        let header = |sig: u32, central: bool| {
            let mut h = Vec::new();
            h.extend_from_slice(&sig.to_le_bytes());
            if central {
                h.extend_from_slice(&20u16.to_le_bytes());
            }
            h.extend_from_slice(&20u16.to_le_bytes());
            h.extend_from_slice(&flags.to_le_bytes());
            h.extend_from_slice(&8u16.to_le_bytes());
            h.extend_from_slice(&self.time.to_le_bytes());
            h.extend_from_slice(&self.date.to_le_bytes());
            h.extend_from_slice(&crc.sum().to_le_bytes());
            h.extend_from_slice(&(data.len() as u32).to_le_bytes());
            h.extend_from_slice(&(content.len() as u32).to_le_bytes());
            h.extend_from_slice(&(name.len() as u16).to_le_bytes());
            h.extend_from_slice(&0u16.to_le_bytes());
            if central {
                h.extend_from_slice(&[0; 6]); // comment, disk, internal attributes
                h.extend_from_slice(&0u32.to_le_bytes());
                h.extend_from_slice(&offset.to_le_bytes());
            }
            h.extend_from_slice(name.as_bytes());
            h
        };
        let local = header(0x0403_4b50, false);
        let central = header(0x0201_4b50, true);
        self.out.extend_from_slice(&local);
        self.out.extend_from_slice(&data);
        self.central.extend_from_slice(&central);
        self.count += 1;
    }

    fn finish(mut self) -> Vec<u8> {
        let offset = self.out.len() as u32;
        let size = self.central.len() as u32;
        self.out.append(&mut self.central);
        self.out.extend_from_slice(&0x0605_4b50u32.to_le_bytes());
        self.out.extend_from_slice(&[0; 4]);
        self.out.extend_from_slice(&self.count.to_le_bytes());
        self.out.extend_from_slice(&self.count.to_le_bytes());
        self.out.extend_from_slice(&size.to_le_bytes());
        self.out.extend_from_slice(&offset.to_le_bytes());
        self.out.extend_from_slice(&0u16.to_le_bytes());
        self.out
    }
}
