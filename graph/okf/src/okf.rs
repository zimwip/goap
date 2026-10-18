//! The OKF tree of a snapshot, written as a zip.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::io::Write;
use std::sync::Arc;

use chrono::{DateTime, Utc};
use goap_domain::parse_type_ref;
use goap_graph::{Snapshot, SnapshotNode};
use serde::Serialize;
use serde_json::{Map, Value};
use serde_yaml_ng::{Mapping, Value as Yaml};
use zip::write::SimpleFileOptions;
use zip::{CompressionMethod, ZipWriter};

/// Format names the OKF layout written here, recorded in the front matter of the index.
pub const FORMAT: &str = "goap.okf/v1";

/// The errors of [`write`]: the writer's, the archive's, the YAML encoding's; a node's carries its key
/// (`node <key>: ...`).
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("{0}")]
    Io(#[from] std::io::Error),
    #[error("{0}")]
    Zip(#[from] zip::result::ZipError),
    #[error("{0}")]
    Yaml(#[from] serde_yaml_ng::Error),
    #[error("node {key}: {source}")]
    Node { key: String, source: Box<Error> },
}

/// BodyAttribute is an attribute of a node type rendered in the Markdown body rather than in the front matter.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct BodyAttribute {
    pub name: String,
    pub label: String,
}

/// The attribute naming the nodes of a type ("": the default).
pub type NameFn = Arc<dyn Fn(&str) -> String + Send + Sync>;
/// The attributes of a type rendered as sections of the body.
pub type BodyFn = Arc<dyn Fn(&str) -> Vec<BodyAttribute> + Send + Sync>;

/// Options say how the nodes of a type are rendered; both are optional (None: the defaults).
#[derive(Clone, Default)]
pub struct Options {
    /// Name is the attribute holding the display name of the nodes of a type (`asName`); "" or None: the first of
    /// "title" and "name" the node carries as a string, else its key.
    pub name: Option<NameFn>,
    /// Body are the attributes rendered as sections of the body (the long texts); None: "description".
    pub body: Option<BodyFn>,
}

// The front matter of a node is flat, so that Obsidian shows it as properties and draws its links (ADR 0111): the
// identity keys below, `aliases` (the key) and `tags` (`<namespace>/<Type>`, `state/<state>`), then one key per
// outgoing link type (its local name) listing wikilinks to the targets' files, then the properties of the node by
// name (a property named like a key already written takes the prefix PROPERTY_PREFIX, unless it holds the same
// value: the title), then `linkProperties`, the properties of the links that carry some.
pub const KEY_KEY: &str = "key";
pub const KEY_TYPE: &str = "type";
pub const KEY_TITLE: &str = "title";
pub const KEY_STATE: &str = "state";
pub const KEY_OWNER: &str = "owner";
pub const KEY_NAMESPACE: &str = "namespace";
pub const KEY_BASELINE: &str = "baseline";
pub const KEY_ALIASES: &str = "aliases";
pub const KEY_TAGS: &str = "tags";
pub const KEY_LINK_PROPERTIES: &str = "linkProperties";
/// KeyProvenance holds where the version comes from (`goap_graph::SnapshotProvenance`): change, producer, and model:
/// true when a model generated it (EU AI Act Art. 50(2)).
pub const KEY_PROVENANCE: &str = "provenance";
pub const PROPERTY_PREFIX: &str = "prop_";

/// LinkProperties are the properties of one outgoing link, in the front matter (`linkProperties`).
#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct LinkProperties {
    #[serde(rename = "type")]
    pub r#type: String,
    pub to: String,
    pub properties: Map<String, Value>,
}

/// An ordered YAML mapping.
struct Ordered(Mapping);

impl Ordered {
    fn has(&self, k: &str) -> bool {
        self.0.contains_key(k)
    }

    /// The string value of a key, when it is a string.
    fn scalar(&self, k: &str) -> Option<&str> {
        self.0.get(k).and_then(Yaml::as_str)
    }

    fn set(&mut self, k: &str, v: impl Serialize) -> Result<(), Error> {
        self.0
            .insert(Yaml::String(k.to_string()), serde_yaml_ng::to_value(v)?);
        Ok(())
    }
}

/// Index is the YAML header of the index of a namespace.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Index {
    pub format: String,
    pub namespace: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub baseline: String,
    #[serde(rename = "exportedAt", with = "goap_domain::gotime")]
    pub exported_at: DateTime<Utc>,
    pub nodes: usize,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub types: BTreeMap<String, usize>,
}

/// IndexFile is the name of the index of a namespace, at the root of its folder.
pub const INDEX_FILE: &str = "index.md";

struct Entry<'a> {
    node: &'a SnapshotNode,
    /// relative to the folder of the namespace
    path: String,
    title: String,
}

struct Incoming<'a> {
    typ: &'a str,
    from: usize,
}

/// Write renders a snapshot as an OKF tree into a zip archive written to w as it goes, one file at a time (nothing
/// but the snapshot is held): `<namespace>/index.md`, then one `<namespace>/<Type>/<key>.md` per node (the key made a
/// safe file name, unique in its folder). at stamps the files.
pub fn write<W: Write>(w: W, s: &Snapshot, o: &Options, at: DateTime<Utc>) -> Result<(), Error> {
    let root = safe_name(&s.namespace);
    let mut entries: Vec<Entry<'_>> = Vec::with_capacity(s.nodes.len());
    let mut by_key: HashMap<&str, usize> = HashMap::new();
    let mut taken: HashSet<String> = HashSet::new();
    for n in &s.nodes {
        let dir = safe_name(type_name(&n.r#type));
        let base = safe_name(&n.key);
        let mut p = format!("{dir}/{base}.md");
        let mut i = 2;
        while taken.contains(&p.to_lowercase()) {
            p = format!("{dir}/{base}-{i}.md");
            i += 1;
        }
        taken.insert(p.to_lowercase());
        by_key.insert(&n.key, entries.len());
        entries.push(Entry {
            node: n,
            path: p,
            title: title_of(n, o),
        });
    }
    let mut incoming: HashMap<&str, Vec<Incoming<'_>>> = HashMap::new();
    for (i, e) in entries.iter().enumerate() {
        for l in &e.node.links {
            incoming.entry(&l.to).or_default().push(Incoming {
                typ: &l.r#type,
                from: i,
            });
        }
    }
    let mut z = ZipWriter::new_stream(w);
    let opts = SimpleFileOptions::default()
        .compression_method(CompressionMethod::Deflated)
        .last_modified_time(zip_time(at));
    let mut put = |name: &str, content: &[u8]| -> Result<(), Error> {
        z.start_file(format!("{root}/{name}"), opts)?;
        z.write_all(content)?;
        Ok(())
    };
    let index = render_index(s, &entries)?;
    put(INDEX_FILE, &index)?;
    for (i, e) in entries.iter().enumerate() {
        let mut inc = incoming.remove(e.node.key.as_str()).unwrap_or_default();
        let content =
            render_node(s, &entries, i, &by_key, &mut inc, o).map_err(|err| Error::Node {
                key: e.node.key.clone(),
                source: Box::new(err),
            })?;
        put(&e.path, &content)?;
    }
    z.finish()?;
    Ok(())
}

/// The zip time of a moment (its UTC wall clock; the range of a zip time clamps it).
fn zip_time(at: DateTime<Utc>) -> zip::DateTime {
    zip::DateTime::try_from(at.naive_utc()).unwrap_or_else(|_| {
        if at.timestamp() < 0 {
            zip::DateTime::default()
        } else {
            zip::DateTime::default_for_write()
        }
    })
}

fn render_node(
    s: &Snapshot,
    entries: &[Entry<'_>],
    at: usize,
    by_key: &HashMap<&str, usize>,
    incoming: &mut [Incoming<'_>],
    o: &Options,
) -> Result<Vec<u8>, Error> {
    let e = &entries[at];
    let n = e.node;
    let body = body_attributes(&n.r#type, o);
    let in_body: HashSet<&str> = body
        .iter()
        .filter(|a| matches!(n.properties.get(&a.name), Some(Value::String(_))))
        .map(|a| a.name.as_str())
        .collect();
    let fm = front_matter(s, entries, at, by_key, &in_body)?;
    let mut b = String::new();
    write_front_matter(&mut b, &Yaml::Mapping(fm.0))?;
    b.push_str(&format!("# {}\n", one_line(&e.title)));
    for a in &body {
        if !in_body.contains(a.name.as_str()) {
            continue;
        }
        let text = n.properties[&a.name].as_str().unwrap_or_default().trim();
        if text.is_empty() {
            continue;
        }
        b.push_str(&format!("\n## {}\n\n{}\n", one_line(&a.label), text));
    }
    if !n.links.is_empty() {
        b.push_str("\n## Links\n\n");
        for l in &n.links {
            let t = by_key.get(l.to.as_str()).map(|&i| &entries[i]);
            let title = t.map_or(l.to.as_str(), |t| t.title.as_str());
            b.push_str(&format!(
                "- {}: [{}]({})\n",
                link_name(&l.r#type),
                one_line(title),
                rel_path(e, t)
            ));
        }
    }
    if !incoming.is_empty() {
        incoming.sort_by(|a, c| {
            a.typ
                .cmp(c.typ)
                .then_with(|| entries[a.from].node.key.cmp(&entries[c.from].node.key))
        });
        b.push_str("\n## Referenced by\n\n");
        for l in incoming.iter() {
            let from = &entries[l.from];
            b.push_str(&format!(
                "- [{}]({}) {}\n",
                one_line(&from.title),
                rel_path(e, Some(from)),
                link_name(l.typ)
            ));
        }
    }
    Ok(b.into_bytes())
}

/// Builds the flat front matter of a node (see [`KEY_KEY`]).
fn front_matter(
    s: &Snapshot,
    entries: &[Entry<'_>],
    at: usize,
    by_key: &HashMap<&str, usize>,
    in_body: &HashSet<&str>,
) -> Result<Ordered, Error> {
    let e = &entries[at];
    let n = e.node;
    let mut m = Ordered(Mapping::new());
    m.set(KEY_KEY, &n.key)?;
    m.set(KEY_TYPE, &n.r#type)?;
    m.set(KEY_TITLE, &e.title)?;
    if !n.state.is_empty() {
        m.set(KEY_STATE, &n.state)?;
    }
    if !n.owner.is_empty() {
        m.set(KEY_OWNER, &n.owner)?;
    }
    m.set(KEY_NAMESPACE, &s.namespace)?;
    if !s.baseline.is_empty() {
        m.set(KEY_BASELINE, &s.baseline)?;
    }
    m.set(KEY_ALIASES, [&n.key])?;
    let mut tags = vec![format!(
        "{}/{}",
        tag_of(&s.namespace),
        tag_of(type_name(&n.r#type))
    )];
    if !n.state.is_empty() {
        tags.push(format!("state/{}", tag_of(&n.state)));
    }
    m.set(KEY_TAGS, &tags)?;
    let mut by_type: HashMap<&str, Vec<String>> = HashMap::new();
    let mut types: Vec<&str> = Vec::new();
    let mut props: Vec<LinkProperties> = Vec::new();
    for l in &n.links {
        let t = type_name(&l.r#type);
        if !by_type.contains_key(t) {
            types.push(t);
        }
        let target = by_key.get(l.to.as_str()).map(|&i| &entries[i]);
        by_type.entry(t).or_default().push(wikilink(target, &l.to));
        if !l.properties.is_empty() {
            props.push(LinkProperties {
                r#type: l.r#type.clone(),
                to: l.to.clone(),
                properties: l.properties.clone(),
            });
        }
    }
    for t in types {
        if !m.has(t) {
            m.set(t, &by_type[t])?;
        }
    }
    if let Some(p) = &n.provenance {
        m.set(KEY_PROVENANCE, p)?;
    }
    let mut keys: Vec<&String> = n.properties.keys().collect();
    keys.sort();
    for k in keys {
        if in_body.contains(k.as_str()) {
            continue;
        }
        let value = &n.properties[k];
        if let Some(v) = m.scalar(k)
            && value.as_str() == Some(v)
        {
            continue; // the same value is already there (the title)
        }
        let name = if m.has(k)
            || k == KEY_LINK_PROPERTIES
            || k == KEY_PROVENANCE
            || k.starts_with(PROPERTY_PREFIX)
        {
            format!("{PROPERTY_PREFIX}{k}")
        } else {
            k.clone()
        };
        m.set(&name, value)?;
    }
    if !props.is_empty() {
        m.set(KEY_LINK_PROPERTIES, &props)?;
    }
    Ok(m)
}

/// An Obsidian link to the file of a node: its path without extension (resolved from the vault root or as a path
/// suffix, so the vault may be the namespace folder or the folder above), its title as label. A target missing from
/// the snapshot is linked by its key.
fn wikilink(t: Option<&Entry<'_>>, key: &str) -> String {
    let Some(t) = t else {
        return format!("[[{key}]]");
    };
    let label = one_line(&t.title)
        .replace('|', "-")
        .replace(']', ")")
        .replace('[', "(");
    format!(
        "[[{}|{}]]",
        t.path.strip_suffix(".md").unwrap_or(&t.path),
        label
    )
}

/// Makes a value a valid tag: letters, digits, '_', '-' and '/' kept, anything else '-'.
fn tag_of(s: &str) -> String {
    s.chars()
        .map(|r| {
            if r.is_ascii_alphanumeric() || r == '_' || r == '-' || r == '/' {
                r
            } else {
                '-'
            }
        })
        .collect()
}

fn render_index(s: &Snapshot, entries: &[Entry<'_>]) -> Result<Vec<u8>, Error> {
    let mut by_type: BTreeMap<&str, Vec<&Entry<'_>>> = BTreeMap::new();
    for e in entries {
        by_type.entry(&e.node.r#type).or_default().push(e);
    }
    let idx = Index {
        format: FORMAT.to_string(),
        namespace: s.namespace.clone(),
        branch: s.branch.clone(),
        baseline: s.baseline.clone(),
        exported_at: s.exported_at,
        nodes: entries.len(),
        types: by_type
            .iter()
            .map(|(t, es)| (t.to_string(), es.len()))
            .collect(),
    };
    let mut b = String::new();
    write_front_matter(&mut b, &serde_yaml_ng::to_value(&idx)?)?;
    b.push_str(&format!("# {}\n", one_line(&s.namespace)));
    if !s.baseline.is_empty() {
        b.push_str(&format!(
            "\nState of baseline `{}` (branch {}).\n",
            s.baseline, s.branch
        ));
    } else {
        b.push_str("\nNothing landed yet: the empty state.\n");
    }
    for (t, es) in by_type.iter_mut() {
        b.push_str(&format!("\n## {t}\n\n"));
        es.sort_by(|a, c| a.node.key.cmp(&c.node.key));
        for e in es.iter() {
            if e.title != e.node.key {
                b.push_str(&format!(
                    "- [{}]({}) `{}`\n",
                    one_line(&e.title),
                    e.path,
                    e.node.key
                ));
            } else {
                b.push_str(&format!("- [{}]({})\n", one_line(&e.title), e.path));
            }
        }
    }
    Ok(b.into_bytes())
}

fn write_front_matter(b: &mut String, v: &Yaml) -> Result<(), Error> {
    b.push_str("---\n");
    b.push_str(&serde_yaml_ng::to_string(v)?);
    b.push_str("---\n\n");
    Ok(())
}

fn body_attributes(typ: &str, o: &Options) -> Vec<BodyAttribute> {
    match &o.body {
        Some(f) => f(typ),
        None => vec![BodyAttribute {
            name: "description".into(),
            label: "Description".into(),
        }],
    }
}

fn title_of(n: &SnapshotNode, o: &Options) -> String {
    let mut names = vec!["title".to_string(), "name".to_string()];
    if let Some(f) = &o.name {
        let a = f(&n.r#type);
        if !a.is_empty() {
            names = vec![a];
        }
    }
    for a in &names {
        if let Some(Value::String(v)) = n.properties.get(a)
            && !v.trim().is_empty()
        {
            return v.trim().to_string();
        }
    }
    n.key.clone()
}

/// The directory of a slash-separated path (`path.Dir` for the relative paths written here).
fn dir_of(p: &str) -> &str {
    p.rfind('/').map_or(".", |i| &p[..i])
}

/// The path of the file of `to` relative to the file of `from` (both one folder below the root).
fn rel_path(from: &Entry<'_>, to: Option<&Entry<'_>>) -> String {
    let Some(to) = to else {
        return String::new();
    };
    if dir_of(&from.path) == dir_of(&to.path) {
        return to.path.rsplit('/').next().unwrap_or_default().to_string();
    }
    format!("../{}", to.path)
}

/// The local name of a qualified type ("alm@Requirement": "Requirement").
fn type_name(typ: &str) -> &str {
    match parse_type_ref(typ) {
        Ok(q) if !q.name.is_empty() => {
            // the name is the suffix of the reference
            &typ[typ.len() - q.name.len()..]
        }
        _ => typ,
    }
}

fn link_name(typ: &str) -> &str {
    type_name(typ)
}

/// Makes a key a portable file name: letters, digits, '.', '_' and '-' kept, anything else '-'; never empty nor a dot
/// name.
fn safe_name(s: &str) -> String {
    let b: String = s
        .chars()
        .map(|r| {
            if r.is_ascii_alphanumeric() || r == '_' || r == '-' || r == '.' {
                r
            } else {
                '-'
            }
        })
        .collect();
    let out = b.trim_matches('.');
    if out.is_empty() {
        "_".to_string()
    } else {
        out.to_string()
    }
}

/// Keeps a title on one line of Markdown.
fn one_line(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_and_tags() {
        assert_eq!(safe_name("NEED:1"), "NEED-1");
        assert_eq!(safe_name(".."), "_");
        assert_eq!(safe_name(".a.b."), "a.b");
        assert_eq!(tag_of("in review/é"), "in-review/-");
        assert_eq!(type_name("alm@Requirement"), "Requirement");
        assert_eq!(type_name("Plain"), "Plain");
        assert_eq!(one_line(" a \n b\t"), "a b");
    }

    #[test]
    fn empty_snapshot() {
        let s = Snapshot {
            namespace: "alm".into(),
            ..Default::default()
        };
        let idx = String::from_utf8(render_index(&s, &[]).unwrap()).unwrap();
        assert!(
            idx.contains("nodes: 0\n") && !idx.contains("types:"),
            "{idx}"
        );
        assert!(
            idx.ends_with("\nNothing landed yet: the empty state.\n"),
            "{idx}"
        );
        let mut buf = Vec::new();
        write(&mut buf, &s, &Options::default(), Utc::now()).unwrap();
        assert!(!buf.is_empty());
    }
}
