//! What the graph reads of the organisation (Go's `lib/access` snapshot, ADR 0039, 0054): the projects as the head of
//! main holds them, in force (a retired node left out), each with the methodologies it names and its parent, so that
//! a project's chain and the methodologies applying to it are answered as the Go service's directory answers them.
use std::collections::HashMap;

use graph_catalog::{Catalog, catalog::STRUCTURE_PROJECT};
use serde_json::Value;

use crate::{
    Result,
    model::{MAIN_BRANCH, NodeRef},
    read,
    store::ReadTx,
};

/// The state of a node its lifecycle retires (`access.StateRetired`).
pub const STATE_RETIRED: &str = "retired";

/// A project of the organisation.
#[derive(Debug, Clone, Default)]
pub struct Project {
    pub methodologies: Vec<String>,
    pub stopped: bool,
}

/// The projects of the organisation, read at the head of main.
#[derive(Debug, Clone, Default)]
pub struct Directory {
    projects: HashMap<String, Project>,
    parent: HashMap<String, String>,
    root: String,
}

impl Directory {
    /// Reads the projects at the head of main of the namespace of their structure.
    pub async fn read<T: ReadTx>(tx: &T, cat: &Catalog) -> Result<Directory> {
        let Some(st) = cat.structure(STRUCTURE_PROJECT) else {
            return Ok(Directory::default());
        };
        let mut out = Directory {
            root: st.root.clone(),
            ..Directory::default()
        };
        let head = read::branch_head(tx, &st.namespace, MAIN_BRANCH).await?;
        let at = read::select(tx, &head, None).await?;
        let refs: Vec<NodeRef> = at.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
        let nodes: Vec<_> = tx
            .nodes_at(&refs, "")
            .await?
            .into_iter()
            .filter(|n| !n.deleted && n.state != STATE_RETIRED && cat.is_a(&n.typ, &st.typ))
            .collect();
        let keys: HashMap<_, _> = nodes.iter().map(|n| (n.id.clone(), n.key.clone())).collect();
        for n in &nodes {
            let props = n.props.as_ref();
            let methodologies = props
                .and_then(|p| p.get("methodologies"))
                .and_then(Value::as_array)
                .map(|l| {
                    l.iter()
                        .filter_map(Value::as_str)
                        .filter(|s| !s.is_empty())
                        .map(str::to_string)
                        .collect()
                })
                .unwrap_or_default();
            let stopped = props.and_then(|p| p.get("stopped")).and_then(Value::as_bool) == Some(true);
            out.projects.insert(n.key.clone(), Project { methodologies, stopped });
        }
        let held: Vec<NodeRef> = nodes.iter().map(|n| n.node_ref()).collect();
        for l in tx.links_from(&held, Some(std::slice::from_ref(&st.parent))).await? {
            let (Some(from), crate::model::Target::Node(to)) = (keys.get(&l.from.id), &l.to) else {
                continue;
            };
            let Some(to) = keys.get(&to.id) else { continue };
            if from != to {
                out.parent.insert(from.clone(), to.clone());
            }
        }
        Ok(out)
    }

    /// A project followed by its ancestors, nearest first, ending with the root (`domain.Hierarchy.Chain`).
    pub fn chain(&self, key: &str) -> Vec<String> {
        let key = if key.is_empty() { self.root.as_str() } else { key };
        let mut out = vec![key.to_string()];
        let mut cur = key;
        while let Some(p) = self.parent.get(cur) {
            if p.is_empty() || out.contains(p) {
                break;
            }
            out.push(p.clone());
            cur = p;
        }
        if !out.contains(&self.root) {
            out.push(self.root.clone());
        }
        out
    }

    /// The methodologies that apply to a project: its own and those of its ancestors, nearest first, no repeat.
    pub fn applicable(&self, project: &str) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for k in self.chain(project) {
            for m in self
                .projects
                .get(&k)
                .map(|p| p.methodologies.as_slice())
                .unwrap_or_default()
            {
                if !out.contains(m) {
                    out.push(m.clone());
                }
            }
        }
        out
    }

    pub fn has_project(&self, key: &str) -> bool {
        self.projects.contains_key(key)
    }

    pub fn stopped(&self, key: &str) -> bool {
        self.projects.get(key).is_some_and(|p| p.stopped)
    }
}
