//! A domain: the object part of the enterprise model, one per namespace (ADR 0013).

use std::borrow::Borrow;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::Path;

use goap_algo::{Algorithm, Instance};
use goap_domain::{Lifecycle, TYPE_SEP};
use serde::{Deserialize, Serialize};

use crate::builtin::builtin_domains;
use crate::error::Error;
use crate::itemtype::ItemType;
use crate::model::{Enum, Issues, LinkType, NAME_RE, NodeType, TypeSet};
use crate::schema::Schema;
use crate::serde_util::null_default;

/// Domain is the object part of the enterprise model: node types and link types, versioned on its own, one per
/// namespace (ADR 0013). Methodologies are the active part (actions, agents, conditions, goals) and use domain types as
/// inputs and outputs.
///
/// Its schema is inlined in its YAML and JSON forms (Go's embedded `Schema`); the struct derefs to it.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(from = "DomainWire", into = "DomainWire")]
pub struct Domain {
    pub name: String,
    pub version: String,
    pub description: String,
    pub schema: Schema,
}

impl std::ops::Deref for Domain {
    type Target = Schema;
    fn deref(&self) -> &Schema {
        &self.schema
    }
}

impl std::ops::DerefMut for Domain {
    fn deref_mut(&mut self) -> &mut Schema {
        &mut self.schema
    }
}

/// The flat form of a domain: no `#[serde(flatten)]`, so that every unknown field is seen by `serde_ignored`.
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
struct DomainWire {
    #[serde(deserialize_with = "null_default")]
    name: String,
    #[serde(deserialize_with = "null_default")]
    version: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    description: String,
    #[serde(deserialize_with = "null_default")]
    node_types: Vec<NodeType>,
    #[serde(deserialize_with = "null_default")]
    link_types: Vec<LinkType>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    enums: Vec<Enum>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    lifecycles: Vec<Lifecycle>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    algorithms: Vec<Algorithm>,
    #[serde(
        rename = "algorithmInstances",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    instances: Vec<Instance>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    item_types: Vec<ItemType>,
}

impl From<DomainWire> for Domain {
    fn from(w: DomainWire) -> Domain {
        Domain {
            name: w.name,
            version: w.version,
            description: w.description,
            schema: Schema {
                node_types: w.node_types,
                link_types: w.link_types,
                enums: w.enums,
                lifecycles: w.lifecycles,
                algorithms: w.algorithms,
                instances: w.instances,
                item_types: w.item_types,
            },
        }
    }
}

impl From<Domain> for DomainWire {
    fn from(d: Domain) -> DomainWire {
        let s = d.schema;
        DomainWire {
            name: d.name,
            version: d.version,
            description: d.description,
            node_types: s.node_types,
            link_types: s.link_types,
            enums: s.enums,
            lifecycles: s.lifecycles,
            algorithms: s.algorithms,
            instances: s.instances,
            item_types: s.item_types,
        }
    }
}

/// ParseDomain decodes a YAML (or JSON) domain. Unknown fields are rejected.
pub fn parse_domain(data: &[u8]) -> Result<Domain, Error> {
    let text = std::str::from_utf8(data).map_err(|e| Error::Parse(e.to_string()))?;
    if text.trim().is_empty() {
        return Err(Error::Parse("EOF".into()));
    }
    let mut unknown: Vec<String> = Vec::new();
    let d: Domain = serde_ignored::deserialize(serde_yaml_ng::Deserializer::from_str(text), |p| {
        let segs = segments(&p);
        if !lenient(&segs) {
            unknown.push(segs.join("."));
        }
    })
    .map_err(|e| Error::Parse(e.to_string()))?;
    if !unknown.is_empty() {
        let fields: Vec<String> = unknown
            .iter()
            .map(|p| {
                format!(
                    "field {} not found (at {p})",
                    p.rsplit('.').next().unwrap_or(p)
                )
            })
            .collect();
        return Err(Error::Parse(fields.join("; ")));
    }
    Ok(d)
}

/// The segments of the path of an ignored field: map keys and sequence indexes, the option wrappers left out.
fn segments(p: &serde_ignored::Path<'_>) -> Vec<String> {
    use serde_ignored::Path;
    let mut out = Vec::new();
    let mut cur = p;
    loop {
        cur = match cur {
            Path::Root => break,
            Path::Seq { parent, index } => {
                out.push(index.to_string());
                parent
            }
            Path::Map { parent, key } => {
                out.push(key.clone());
                parent
            }
            Path::Some { parent }
            | Path::NewtypeStruct { parent }
            | Path::NewtypeVariant { parent } => parent,
        };
    }
    out.reverse();
    out
}

/// Whether an ignored field lies inside a definition yaml.v3 decodes without its known-fields check: the node types,
/// attributes and enum values, whose shorthand (a plain name) Go reads with a custom `UnmarshalYAML` that decodes the
/// object again with a fresh decoder. Unknown fields there are tolerated, as in Go.
fn lenient(segs: &[String]) -> bool {
    let index = |s: &String| s.parse::<usize>().is_ok();
    if segs.len() >= 3 && segs[0] == "nodeTypes" && index(&segs[1]) {
        return true;
    }
    if segs.len() >= 5
        && segs[0] == "enums"
        && index(&segs[1])
        && segs[2] == "values"
        && index(&segs[3])
    {
        return true;
    }
    segs.windows(3)
        .any(|w| w[0] == "attributes" && index(&w[1]))
}

impl Domain {
    /// YAML renders the domain.
    pub fn yaml(&self) -> Result<String, Error> {
        serde_yaml_ng::to_string(self).map_err(|e| Error::Render(e.to_string()))
    }

    /// Validate returns every problem of the domain (empty when valid).
    pub fn validate(&self) -> Issues {
        let mut issues = Issues::default();
        if self.name.is_empty() {
            issues.add("name", "name required");
        } else if !NAME_RE.is_match(&self.name) {
            issues.add(
                "name",
                "name must be lowercase letters, digits, '-' or '_' and start with a letter",
            );
        }
        if self.node_types.is_empty() {
            issues.add("nodeTypes", "at least one node type required");
        }
        self.schema.check("", &mut issues);
        issues
    }
}

/// The TypeSet of a list of domains and of the built-in domains ([`domain_types`]).
#[derive(Debug, Clone, Default)]
pub struct DomainTypeSet {
    nodes: HashSet<String>,
    links: HashSet<String>,
    parents: HashMap<String, String>,
}

/// DomainTypes is the TypeSet of a list of domains and of the built-in domains: a bare name inside a domain is a
/// type of that domain. The registry and the services use the type catalogue (goap-typecat); this is for files and
/// tests.
pub fn domain_types<D: Borrow<Domain>>(ds: &[D]) -> DomainTypeSet {
    let mut t = DomainTypeSet::default();
    for d in builtin_domains()
        .iter()
        .chain(ds.iter().map(|d| d.borrow()))
    {
        for n in &d.node_types {
            let r = format!("{}{TYPE_SEP}{}", d.name, n.name);
            if !n.extends.is_empty()
                && let Ok(p) = goap_domain::qualify_in(&d.name, &n.extends)
            {
                t.parents.insert(r.clone(), p.to_string());
            }
            t.nodes.insert(r);
        }
        for l in &d.link_types {
            t.links.insert(format!("{}{TYPE_SEP}{}", d.name, l.name));
        }
    }
    t
}

impl TypeSet for DomainTypeSet {
    fn has_node_type(&self, r#ref: &str) -> bool {
        self.nodes.contains(r#ref)
    }

    fn has_link_type(&self, r#ref: &str) -> bool {
        self.links.contains(r#ref)
    }

    fn supertypes(&self) -> BTreeMap<String, Vec<String>> {
        let mut out = BTreeMap::new();
        for n in &self.nodes {
            let mut seen: HashSet<&str> = HashSet::from([n.as_str()]);
            let mut chain = Vec::new();
            let mut p = self.parents.get(n).map(String::as_str).unwrap_or("");
            while !p.is_empty() && seen.insert(p) {
                chain.push(p.to_string());
                p = self.parents.get(p).map(String::as_str).unwrap_or("");
            }
            if !chain.is_empty() {
                out.insert(n.clone(), chain);
            }
        }
        out
    }
}

/// LoadDomains parses the domain files (`*.y*ml`) of a directory, in name order.
pub fn load_domains(dir: impl AsRef<Path>) -> Result<Vec<Domain>, Error> {
    let dir = dir.as_ref();
    let mut files: Vec<_> = match std::fs::read_dir(dir) {
        Ok(rd) => rd
            .filter_map(|e| e.ok())
            .map(|e| e.path())
            .filter(|p| {
                p.file_name()
                    .and_then(|n| n.to_str())
                    .is_some_and(|n| n.ends_with("ml") && n[..n.len() - 2].contains(".y"))
            })
            .collect(),
        // filepath.Glob ignores a directory it cannot read
        Err(_) => Vec::new(),
    };
    files.sort();
    let mut out = Vec::new();
    for f in files {
        let src = std::fs::read(&f)?;
        let d = parse_domain(&src).map_err(|e| Error::File {
            file: f.display().to_string(),
            source: Box::new(e),
        })?;
        out.push(d);
    }
    Ok(out)
}
