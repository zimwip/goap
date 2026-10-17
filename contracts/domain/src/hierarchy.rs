//! Hierarchy is one structure of the graph read as a tree of node keys (ADR 0054, 0068).

use std::collections::{HashMap, HashSet};

use crate::{Link, Node, NodeID, Structures};

/// Hierarchy is one structure of the graph read as a tree of node keys (ADR 0054, 0068): the child -> parent links of
/// the organisation or of the projects, with the root every chain ends with. It is the one place that walks them:
/// pkg/access (roles), internal/mcpsvc (adapters) and the goap-admin listings resolve a chain through it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Hierarchy {
    root: String,
    parent: HashMap<String, String>,
}

impl Hierarchy {
    /// NewHierarchy returns an empty hierarchy whose chains end with root.
    pub fn new(root: impl Into<String>) -> Self {
        Hierarchy {
            root: root.into(),
            parent: HashMap::new(),
        }
    }

    /// Add records the parent of a node; a self link is no parent.
    pub fn add(&mut self, child: impl Into<String>, parent: impl Into<String>) {
        let (child, parent) = (child.into(), parent.into());
        if child != parent {
            self.parent.insert(child, parent);
        }
    }

    /// Root is the key every chain ends with.
    pub fn root(&self) -> &str {
        &self.root
    }

    /// Parent is the node a node is part of; empty when it has none (the root, or a node not placed).
    pub fn parent(&self, key: &str) -> &str {
        self.parent.get(key).map_or("", String::as_str)
    }

    /// Chain returns a node followed by its ancestors, nearest first, ending with the root (appended when the walk did
    /// not reach it: a node with no parent link inherits from the root). An empty key stands for the root. The walk
    /// stops at a cycle.
    pub fn chain(&self, key: &str) -> Vec<String> {
        let key = if key.is_empty() {
            self.root.as_str()
        } else {
            key
        };
        let mut out = vec![key.to_string()];
        let mut seen: HashSet<&str> = HashSet::from([key]);
        let mut cur = key;
        while let Some(p) = self.parent.get(cur) {
            if p.is_empty() || !seen.insert(p.as_str()) {
                break;
            }
            out.push(p.clone());
            cur = p;
        }
        if !seen.contains(self.root.as_str()) {
            out.push(self.root.clone());
        }
        out
    }
}

impl Structures {
    /// Hierarchy reads the structure of a kind from the nodes and links of the graph: the parent links the structure
    /// names, between nodes of its types (the tagged type and its subtypes), a self link (the root project) never
    /// being a parent. Nodes outside the structure and links of other types are ignored.
    pub fn hierarchy(&self, kind: &str, nodes: &[Node], links: &[Link]) -> Hierarchy {
        let st = self.of(kind);
        let mut h = Hierarchy::new(st.root.clone());
        let by_id: HashMap<&NodeID, &Node> = nodes.iter().map(|n| (&n.id, n)).collect();
        let empty = Node::default();
        for l in links {
            let from = by_id.get(&l.from.id).copied().unwrap_or(&empty);
            let to = by_id.get(&l.to.id).copied().unwrap_or(&empty);
            if l.r#type == st.parent
                && self.includes(kind, &from.r#type)
                && self.includes(kind, &to.r#type)
            {
                h.add(from.key.clone(), to.key.clone());
            }
        }
        h
    }
}
