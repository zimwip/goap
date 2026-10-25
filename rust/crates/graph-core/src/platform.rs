//! The namespaces of the platform when another graph holds them (ADR 0154 §9, Go's `Graph.Directory`): a content graph
//! places its changes and its versions in the organisation and the projects of the platform graph, and asks the
//! roles of the methodologies of its changes there. What it read of the platform graph, the head of main of those
//! namespaces, travels with the transaction ([`crate::store::ReadTx::platform`]): the reads of the organisation, the
//! projects and the methodology registry take it instead of the store.
use std::collections::{HashMap, HashSet};

use graph_catalog::Catalog;

use crate::model::{Link, Node, Target};

/// A namespace at the head of main of the platform graph: its nodes (deleted ones left out) and the links leaving them.
#[derive(Debug, Clone, Default)]
pub struct Held {
    /// The baseline the nodes were read at (empty: the empty state).
    pub baseline: String,
    pub nodes: Vec<Node>,
    pub links: Vec<Link>,
}

/// What a content graph read of the platform graph.
#[derive(Debug, Clone, Default)]
pub struct Platform {
    /// The organisation and the projects (the namespace of their structures).
    pub organisation: Held,
    /// The versions of the methodologies and their roles (`methodology@MethodologyVersion`, `methodology@Role`).
    pub methodology: Held,
}

impl Platform {
    /// The node of a structure of a kind by its key, retired or not (Go's `Directory.StructureNode`).
    pub fn structure_node(&self, cat: &Catalog, kind: &str, key: &str) -> Option<&Node> {
        let st = cat.structure(kind)?;
        self.organisation
            .nodes
            .iter()
            .find(|n| n.key == key && cat.is_a(&n.typ, &st.typ))
    }

    /// A node of a structure followed by its ancestors through the parent links of the structure, nearest first, the
    /// root last (Go's `Directory.StructureChain`).
    pub fn structure_chain(&self, cat: &Catalog, kind: &str, key: &str) -> Vec<String> {
        let Some(st) = cat.structure(kind) else {
            return vec![key.to_string()];
        };
        let in_st = |n: &&Node| cat.is_a(&n.typ, &st.typ);
        let keys: HashMap<_, _> = self
            .organisation
            .nodes
            .iter()
            .filter(in_st)
            .map(|n| (n.id.clone(), n.key.clone()))
            .collect();
        let mut parent: HashMap<&str, &str> = HashMap::new();
        for l in &self.organisation.links {
            if l.typ != st.parent {
                continue;
            }
            let (Some(from), Target::Node(to)) = (keys.get(&l.from.id), &l.to) else {
                continue;
            };
            if let Some(to) = keys.get(&to.id)
                && from != to
            {
                parent.entry(from.as_str()).or_insert(to.as_str());
            }
        }
        let key = if key.is_empty() { st.root.as_str() } else { key };
        let mut out = vec![key.to_string()];
        let mut seen = HashSet::from([key]);
        let mut cur = key;
        while let Some(p) = parent.get(cur) {
            if !seen.insert(p) {
                break;
            }
            out.push(p.to_string());
            cur = p;
        }
        if !out.contains(&st.root) {
            out.push(st.root.clone());
        }
        out
    }
}
