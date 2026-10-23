//! The shape of the state at the head of a branch (Go's `graph/graph/stats.go`): nodes, links, degrees, components,
//! computed from that state, nothing stored.
use std::collections::{BTreeMap, HashMap, HashSet};

use crate::model::{Branch, Link, MAIN_BRANCH, Node, NodeId, Version};

/// The measures of the store, in the order they are shown (`graph.StoreMeasures`).
pub const STORE_MEASURES: [&str; 12] = [
    "nodesByType",
    "versionsByType",
    "versionsByReason",
    "versionsPerNode",
    "linkRowsByType",
    "linkDrops",
    "linkSetsByMode",
    "joins",
    "baselinesByKind",
    "changesByStatus",
    "tags",
    "logByType",
];

/// The branches of a namespace counted.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct BranchStats {
    pub total: usize,
    pub lasting: usize,
    pub ephemeral: usize,
    pub by_status: HashMap<String, usize>,
    /// the lasting branches, main first, then by name; main is there before its first baseline
    pub names: Vec<String>,
}

pub fn branch_stats(bs: &[Branch]) -> BranchStats {
    let mut out = BranchStats {
        total: bs.len(),
        ..BranchStats::default()
    };
    for b in bs {
        *out.by_status.entry(b.status.clone()).or_default() += 1;
        if b.ephemeral {
            out.ephemeral += 1;
        } else {
            out.lasting += 1;
            out.names.push(b.name.clone());
        }
    }
    if !out.names.iter().any(|n| n == MAIN_BRANCH) {
        out.names.push(MAIN_BRANCH.into());
    }
    out.names
        .sort_by(|a, b| (a != MAIN_BRANCH, a).cmp(&(b != MAIN_BRANCH, b)));
    out
}

/// A list of counts summarized.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Distribution {
    pub min: i64,
    pub max: i64,
    pub mean: f64,
    pub median: f64,
    pub p90: i64,
    pub sum: i64,
}

pub fn distribution(xs: &[i64]) -> Distribution {
    if xs.is_empty() {
        return Distribution::default();
    }
    let mut s = xs.to_vec();
    s.sort_unstable();
    let n = s.len();
    let sum: i64 = s.iter().sum();
    Distribution {
        min: s[0],
        max: s[n - 1],
        mean: sum as f64 / n as f64,
        median: if n % 2 == 1 {
            s[n / 2] as f64
        } else {
            (s[n / 2 - 1] + s[n / 2]) as f64 / 2.0
        },
        p90: s[(0.9 * n as f64).ceil() as usize - 1],
        sum,
    }
}

/// A range of the histogram of degrees: `[from, to]`, `to` 0 for no upper bound.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Bucket {
    pub label: String,
    pub from: i64,
    pub to: i64,
    pub count: i64,
}

const DEGREE_BUCKETS: [(i64, i64); 9] = [
    (0, 0),
    (1, 1),
    (2, 2),
    (3, 5),
    (6, 10),
    (11, 20),
    (21, 50),
    (51, 100),
    (101, 0),
];

fn histogram(degrees: &[i64]) -> Vec<Bucket> {
    let mut out: Vec<Bucket> = DEGREE_BUCKETS
        .iter()
        .map(|&(from, to)| Bucket {
            label: match () {
                _ if to == 0 && from > 0 => format!("{from}+"),
                _ if to != from => format!("{from}–{to}"),
                _ => from.to_string(),
            },
            from,
            to,
            count: 0,
        })
        .collect();
    for d in degrees {
        if let Some(i) = DEGREE_BUCKETS.iter().rposition(|(from, _)| d >= from) {
            out[i].count += 1;
        }
    }
    out
}

/// The shape of the nodes of one type.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct TypeStats {
    pub typ: String,
    pub nodes: usize,
    pub states: HashMap<String, usize>,
    pub out: Distribution,
    pub r#in: Distribution,
    pub versions: Distribution,
    pub properties: Distribution,
    pub isolated: usize,
}

/// The links from a node type to another.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct TypePair {
    pub from: String,
    pub to: String,
    pub count: usize,
}

/// The shape of the links of one type.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct LinkTypeStats {
    pub typ: String,
    pub links: usize,
    pub suspect: usize,
    pub sources: usize,
    pub targets: usize,
    pub pairs: Vec<TypePair>,
    pub max_out: usize,
}

/// A node with many links.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Hub {
    pub id: NodeId,
    pub key: String,
    pub typ: String,
    pub out: usize,
    pub r#in: usize,
}

/// The shape of a state (`graph.StateStats`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct StateStats {
    pub nodes: usize,
    pub deleted: usize,
    pub links: usize,
    pub suspect: usize,
    pub external: usize,
    pub isolated: usize,
    pub self_links: usize,
    pub density: f64,
    pub component_count: usize,
    /// the largest first, at most 20
    pub components: Vec<usize>,
    pub out: Distribution,
    pub r#in: Distribution,
    pub degree: Distribution,
    pub histogram: Vec<Bucket>,
    pub versions: Distribution,
    pub properties: Distribution,
    pub types: Vec<TypeStats>,
    pub link_types: Vec<LinkTypeStats>,
    /// at most 10
    pub hubs: Vec<Hub>,
    pub owners: usize,
    pub projects: usize,
    pub reciprocal: usize,
}

#[derive(Default)]
struct TypeAcc {
    out: Vec<i64>,
    r#in: Vec<i64>,
    versions: Vec<i64>,
    props: Vec<i64>,
    states: HashMap<String, usize>,
    isolated: usize,
}

#[derive(Default)]
struct LinkAcc {
    stats: LinkTypeStats,
    sources: HashSet<NodeId>,
    targets: HashSet<NodeId>,
    pairs: HashMap<(String, String), usize>,
    per_node: HashMap<NodeId, usize>,
}

/// Computes the shape of a state from its nodes and the links leaving them (`stateStats`).
pub fn state_stats(head: &HashMap<NodeId, Version>, nodes: &[Node], links: &[Link]) -> StateStats {
    let mut s = StateStats::default();
    let mut live: HashMap<&NodeId, &Node> = HashMap::new();
    for n in nodes {
        if n.deleted {
            s.deleted += 1;
        } else {
            live.insert(&n.id, n);
        }
    }
    s.nodes = live.len();
    let (mut out, mut inn): (HashMap<&NodeId, usize>, HashMap<&NodeId, usize>) = Default::default();
    let mut by_type: BTreeMap<&str, LinkAcc> = BTreeMap::new();
    let mut edges: HashSet<(&NodeId, &NodeId)> = HashSet::new();
    let mut uf = UnionFind::new(live.keys().copied());
    for l in links {
        let Some(from) = live.get(&l.from.id) else {
            continue;
        };
        let lt = by_type.entry(&l.typ).or_insert_with(|| LinkAcc {
            stats: LinkTypeStats {
                typ: l.typ.clone(),
                ..LinkTypeStats::default()
            },
            ..LinkAcc::default()
        });
        let target = l.to_node().and_then(|t| Some((head.get(&t.id)?, t, live.get(&t.id)?)));
        let Some((&v, t, to)) = target else {
            s.external += 1;
            continue;
        };
        if v != t.version {
            s.suspect += 1;
            lt.stats.suspect += 1;
        }
        s.links += 1;
        lt.stats.links += 1;
        *out.entry(&from.id).or_default() += 1;
        *inn.entry(&to.id).or_default() += 1;
        lt.sources.insert(from.id.clone());
        lt.targets.insert(to.id.clone());
        *lt.pairs.entry((from.typ.clone(), to.typ.clone())).or_default() += 1;
        *lt.per_node.entry(from.id.clone()).or_default() += 1;
        if from.id == to.id {
            s.self_links += 1;
        }
        edges.insert((&from.id, &to.id));
        uf.union(&from.id, &to.id);
    }
    s.reciprocal = edges.iter().filter(|(a, b)| a < b && edges.contains(&(*b, *a))).count();
    if s.nodes > 1 {
        s.density = s.links as f64 / (s.nodes as f64 * (s.nodes - 1) as f64);
    }
    // the nodes, in a stable order
    let mut ids: Vec<&NodeId> = live.keys().copied().collect();
    ids.sort_by(|a, b| live[a].key.cmp(&live[b].key));
    let mut types: HashMap<&str, TypeAcc> = HashMap::new();
    let (mut outs, mut ins, mut degrees, mut versions, mut props) = (vec![], vec![], vec![], vec![], vec![]);
    let (mut owners, mut projects) = (HashSet::new(), HashSet::new());
    for id in &ids {
        let n = live[id];
        let (o, i) = (
            out.get(id).copied().unwrap_or(0) as i64,
            inn.get(id).copied().unwrap_or(0) as i64,
        );
        let np = n.props.as_ref().map_or(0, |p| p.len()) as i64;
        outs.push(o);
        ins.push(i);
        degrees.push(o + i);
        versions.push(n.version as i64);
        props.push(np);
        let t = types.entry(&n.typ).or_default();
        t.out.push(o);
        t.r#in.push(i);
        t.versions.push(n.version as i64);
        t.props.push(np);
        if !n.state.is_empty() {
            *t.states.entry(n.state.clone()).or_default() += 1;
        }
        if o + i == 0 {
            s.isolated += 1;
            t.isolated += 1;
        }
        if !n.owner.is_zero() {
            owners.insert(n.owner.to_string());
        }
        if !n.project.is_zero() {
            projects.insert(n.project.to_string());
        }
    }
    s.owners = owners.len();
    s.projects = projects.len();
    s.out = distribution(&outs);
    s.r#in = distribution(&ins);
    s.degree = distribution(&degrees);
    s.versions = distribution(&versions);
    s.properties = distribution(&props);
    s.histogram = histogram(&degrees);
    s.types = types
        .into_iter()
        .map(|(name, t)| TypeStats {
            typ: name.to_string(),
            nodes: t.out.len(),
            out: distribution(&t.out),
            r#in: distribution(&t.r#in),
            versions: distribution(&t.versions),
            properties: distribution(&t.props),
            states: t.states,
            isolated: t.isolated,
        })
        .collect();
    s.types
        .sort_by(|a, b| b.nodes.cmp(&a.nodes).then_with(|| a.typ.cmp(&b.typ)));
    s.link_types = by_type
        .into_values()
        .map(|acc| {
            let mut lt = acc.stats;
            lt.sources = acc.sources.len();
            lt.targets = acc.targets.len();
            lt.pairs = acc
                .pairs
                .into_iter()
                .map(|((from, to), count)| TypePair { from, to, count })
                .collect();
            lt.pairs.sort_by(|a, b| {
                b.count
                    .cmp(&a.count)
                    .then_with(|| format!("{}{}", a.from, a.to).cmp(&format!("{}{}", b.from, b.to)))
            });
            lt.max_out = acc.per_node.values().copied().max().unwrap_or(0);
            lt
        })
        .collect();
    s.link_types
        .sort_by(|a, b| b.links.cmp(&a.links).then_with(|| a.typ.cmp(&b.typ)));
    let degree = |id: &NodeId| out.get(id).copied().unwrap_or(0) + inn.get(id).copied().unwrap_or(0);
    let mut hubs = ids.clone();
    hubs.sort_by_key(|id| std::cmp::Reverse(degree(id)));
    for id in hubs.into_iter().take(10) {
        if degree(id) == 0 {
            break;
        }
        let n = live[id];
        s.hubs.push(Hub {
            id: id.clone(),
            key: n.key.clone(),
            typ: n.typ.clone(),
            out: out.get(id).copied().unwrap_or(0),
            r#in: inn.get(id).copied().unwrap_or(0),
        });
    }
    let mut sizes = uf.sizes();
    s.component_count = sizes.len();
    sizes.truncate(20);
    s.components = sizes;
    s
}

/// The nodes of a state grouped into weakly connected components.
struct UnionFind<'a> {
    parent: HashMap<&'a NodeId, &'a NodeId>,
    size: HashMap<&'a NodeId, usize>,
}

impl<'a> UnionFind<'a> {
    fn new(ids: impl Iterator<Item = &'a NodeId>) -> Self {
        let mut u = UnionFind {
            parent: HashMap::new(),
            size: HashMap::new(),
        };
        for id in ids {
            u.parent.insert(id, id);
            u.size.insert(id, 1);
        }
        u
    }

    fn find(&mut self, mut id: &'a NodeId) -> &'a NodeId {
        while self.parent[id] != id {
            let grand = self.parent[self.parent[id]];
            self.parent.insert(id, grand);
            id = grand;
        }
        id
    }

    fn union(&mut self, a: &'a NodeId, b: &'a NodeId) {
        let (mut ra, mut rb) = (self.find(a), self.find(b));
        if ra == rb {
            return;
        }
        if self.size[ra] < self.size[rb] {
            std::mem::swap(&mut ra, &mut rb);
        }
        self.parent.insert(rb, ra);
        let n = self.size[rb];
        *self.size.get_mut(ra).unwrap() += n;
    }

    /// The size of every component, the largest first.
    fn sizes(&self) -> Vec<usize> {
        let mut out: Vec<usize> = self
            .parent
            .iter()
            .filter(|(id, p)| id == p)
            .map(|(id, _)| self.size[id])
            .collect();
        out.sort_unstable_by(|a, b| b.cmp(a));
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn distributions_and_buckets_are_go_s() {
        let d = distribution(&[3, 1, 2, 10]);
        assert_eq!((d.min, d.max, d.sum, d.median, d.p90), (1, 10, 16, 2.5, 10));
        assert_eq!(d.mean, 4.0);
        let h = histogram(&[0, 1, 4, 200]);
        let labels: Vec<_> = h.iter().map(|b| (b.label.as_str(), b.count)).collect();
        assert_eq!(labels[0], ("0", 1));
        assert_eq!(labels[3], ("3–5", 1));
        assert_eq!(labels[8], ("101+", 1));
    }
}
