//! What a caller reads of the current graph (Go's `graph.Reach`, ADR 0161 §4-5): the versions whose scope
//! (`Node.visibility`) reaches it, every node of the open namespaces. No reach (`None`) reads everything: a platform
//! service acting by itself.
use std::collections::{HashMap, HashSet};

use crate::access::{Principal, ReachSet, Snapshot, TYPE_ORG_UNIT, TYPE_PROJECT_UNIT};
use crate::lnk::Url;
use crate::model::{
    Link, Node, NodeId, ReachFilter, VISIBILITY_PERSONAL, VISIBILITY_PRIVATE, VISIBILITY_PUBLIC, VISIBILITY_TREE,
    namespace_of, visibility_of,
};

/// The namespaces read whole: the built-in organisation, platform and methodology describe the platform
/// (`graphsvc.openNamespaces`).
pub const OPEN_NAMESPACES: [&str; 3] = ["organisation", "platform", "methodology"];

#[derive(Clone, Debug, Default)]
pub struct Reach {
    /// The keys of the projects the caller works on: their versions of scope project, and of scope private or
    /// personal when it is their responsible unit or their creator.
    pub projects: HashSet<String>,
    /// The keys of the projects whose versions of scope tree it reads (the parent tree of a project it works on).
    pub trees: HashSet<String>,
    /// The keys of its units (its personal unit, its unit and the units above it): the private versions they answer
    /// for.
    pub units: HashSet<String>,
    /// The caller: the personal versions of the nodes it created.
    pub subject: String,
}

impl Reach {
    /// The reach of a principal (`graphsvc.ReachOf` over `ReachSet`).
    pub fn of(snap: &Snapshot, p: &Principal) -> Reach {
        Reach::from_set(snap.reach_set(p))
    }

    /// The reach a reach set compiles to.
    pub fn from_set(r: ReachSet) -> Reach {
        Reach {
            projects: r.projects.into_iter().collect(),
            trees: r.trees.into_iter().collect(),
            units: r.units.into_iter().collect(),
            subject: r.subject,
        }
    }

    /// The reach in a namespace (`Reach.in`): none when it is read whole.
    pub fn within<'a>(reach: Option<&'a Reach>, namespace: &str) -> Option<&'a Reach> {
        reach.filter(|_| !OPEN_NAMESPACES.contains(&namespace_of(namespace)))
    }

    /// Whether the reach holds a version: its scope reaches the caller (`Reach.sees`, ADR 0161 §4).
    pub fn sees(&self, n: &Node) -> bool {
        let p = &n.project.key;
        match visibility_of(&n.visibility) {
            VISIBILITY_PUBLIC => true,
            VISIBILITY_TREE => self.trees.contains(p) || self.projects.contains(p),
            VISIBILITY_PRIVATE => self.projects.contains(p) && self.units.contains(&n.responsible.key),
            VISIBILITY_PERSONAL => {
                self.projects.contains(p) && !self.subject.is_empty() && n.creator.subject == self.subject
            }
            _ => self.projects.contains(p),
        }
    }

    /// Whether a reach holds a node of any namespace (`Reach.Sees`).
    pub fn holds(reach: Option<&Reach>, n: &Node) -> bool {
        Reach::within(reach, &n.namespace).is_none_or(|r| r.sees(n))
    }

    /// The reach as the stores filter it (`filterOf`): the keys as the nodes store them, their logical URLs and their
    /// bare keys, sorted.
    pub fn filter(&self) -> ReachFilter {
        let keys = |typ: &str, set: &HashSet<String>| {
            let (ns, t) = typ.split_once('@').unwrap_or_default();
            let mut out: Vec<String> = set
                .iter()
                .flat_map(|k| [k.clone(), Url::new(ns, t, k).to_string()])
                .collect();
            out.sort();
            out.dedup();
            out
        };
        ReachFilter {
            projects: keys(TYPE_PROJECT_UNIT, &self.projects),
            trees: keys(TYPE_PROJECT_UNIT, &self.trees),
            units: keys(TYPE_ORG_UNIT, &self.units),
            subject: self.subject.clone(),
        }
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
