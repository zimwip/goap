//! Pages of links and nodes, and the counts per type that go with them.
use std::collections::BTreeMap;

use crate::model::{Link, Node, NodePage, NodeQuery, TypeCount};

/// Counts by type, sorted by type, and the total of `typ` (every type when empty).
pub fn type_counts(counts: &BTreeMap<String, usize>, typ: &str) -> (Vec<TypeCount>, usize) {
    let mut total = 0;
    let types = counts
        .iter()
        .map(|(t, &count)| {
            if typ.is_empty() || t == typ {
                total += count;
            }
            TypeCount { typ: t.clone(), count }
        })
        .collect();
    (types, total)
}

/// A page of links: ordered by type then id, those of `typ` (empty: every type) from `offset`, at most `limit`, their
/// number, and the count of each type.
pub fn link_page(links: &[Link], typ: &str, offset: usize, limit: usize) -> (Vec<Link>, usize, Vec<TypeCount>) {
    let mut sorted: Vec<&Link> = links.iter().collect();
    sorted.sort_by(|a, b| a.typ.cmp(&b.typ).then_with(|| a.id.cmp(&b.id)));
    let mut counts = BTreeMap::new();
    let mut kept = Vec::new();
    for l in sorted {
        *counts.entry(l.typ.clone()).or_insert(0) += 1;
        if typ.is_empty() || l.typ == typ {
            kept.push(l);
        }
    }
    let total = kept.len();
    let page = kept.into_iter().skip(offset).take(limit).cloned().collect();
    (page, total, type_counts(&counts, "").0)
}

/// A page of nodes already read: those `q` keeps, in their order.
pub fn page_nodes(nodes: Vec<Node>, q: &NodeQuery) -> NodePage {
    let text = q.text.trim().to_lowercase();
    let mut counts = BTreeMap::new();
    let mut matched = Vec::new();
    for n in nodes {
        if (n.deleted && !q.include_deleted) || (!text.is_empty() && !n.matches(&text)) || !in_projects(&n, &q.projects)
        {
            continue;
        }
        *counts.entry(n.typ.clone()).or_insert(0) += 1;
        if q.typ.is_empty() || n.typ == q.typ {
            matched.push(n);
        }
    }
    let (types, total) = type_counts(&counts, &q.typ);
    NodePage {
        nodes: matched.into_iter().skip(q.offset).take(q.limit).collect(),
        total,
        types,
        ..NodePage::default()
    }
}

/// Whether a node is of one of some projects as the nodes store them (None: every project).
pub fn in_projects(n: &Node, projects: &Option<Vec<String>>) -> bool {
    match projects {
        None => true,
        Some(ps) => {
            let url = n.project.to_string();
            ps.iter().any(|p| *p == url || *p == n.project.key)
        }
    }
}
