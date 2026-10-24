//! What a caller reads of the current graph (Go's `graph.Reach`, ADR 0161 §5, phase 1: the project rule): the nodes
//! of the projects it works on, every node of the open namespaces. No reach (`None`) reads everything: a platform
//! service acting by itself.
use std::collections::{HashMap, HashSet};

use crate::access::{Principal, Snapshot, TYPE_PROJECT_UNIT};
use crate::lnk::Url;
use crate::model::{Link, Node, NodeId, namespace_of};

/// The namespaces read whole: the built-in organisation, platform and methodology describe the platform
/// (`graphsvc.openNamespaces`).
pub const OPEN_NAMESPACES: [&str; 3] = ["organisation", "platform", "methodology"];

#[derive(Clone, Debug, Default)]
pub struct Reach {
    /// The keys of the projects whose nodes the caller reads.
    pub projects: HashSet<String>,
}

impl Reach {
    /// The reach of a principal (`graphsvc.ReachOf` over `ReachableProjects`).
    pub fn of(snap: &Snapshot, p: &Principal) -> Reach {
        Reach {
            projects: snap.reachable_projects(p).into_iter().collect(),
        }
    }

    /// The reach in a namespace (`Reach.in`): none when it is read whole.
    pub fn within<'a>(reach: Option<&'a Reach>, namespace: &str) -> Option<&'a Reach> {
        reach.filter(|_| !OPEN_NAMESPACES.contains(&namespace_of(namespace)))
    }

    /// Whether the reach holds a node: a node of a project it works on (`Reach.sees`).
    pub fn sees(&self, n: &Node) -> bool {
        self.projects.contains(&n.project.key)
    }

    /// Whether a reach holds a node of any namespace (`Reach.Sees`).
    pub fn holds(reach: Option<&Reach>, n: &Node) -> bool {
        Reach::within(reach, &n.namespace).is_none_or(|r| r.sees(n))
    }

    /// The reach as the stores filter it (`projectsOf`): the projects as the nodes store them, their logical URLs
    /// and their bare keys, sorted.
    pub fn filter(&self) -> Vec<String> {
        let (ns, typ) = TYPE_PROJECT_UNIT.split_once('@').unwrap_or_default();
        let mut out: Vec<String> = self
            .projects
            .iter()
            .flat_map(|k| [k.clone(), Url::new(ns, typ, k).to_string()])
            .collect();
        out.sort();
        out.dedup();
        out
    }

    /// Keeps the nodes a reach holds and the links none of whose ends is a node it leaves out (`Reach.Keep`: no stub,
    /// a link to a node out of reach tells the node exists).
    pub fn keep(reach: Option<&Reach>, mut nodes: Vec<Node>, mut links: Vec<Link>) -> (Vec<Node>, Vec<Link>) {
        if reach.is_none() {
            return (nodes, links);
        }
        let mut hidden: HashMap<NodeId, bool> = HashMap::new();
        nodes.retain(|n| {
            let h = !Reach::holds(reach, n);
            hidden.insert(n.id.clone(), h);
            !h
        });
        let hid = |id: &NodeId| hidden.get(id).copied().unwrap_or(false);
        links.retain(|l| !hid(&l.from.id) && l.to_node().is_none_or(|t| !hid(&t.id)));
        (nodes, links)
    }
}
