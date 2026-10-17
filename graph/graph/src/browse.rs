//! Port of `browse.go`. Browsing a baseline without shipping it whole: the IDE lists the node types of a baseline,
//! pages through the nodes of one type with a text filter, and shows the neighbourhood of a selected node.

use std::collections::{BTreeMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{BaselineID, Link, LinkID, Node, NodeID, NodeRef, Version};
use serde_json::Value;

use crate::graph::links_within;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// MaxBrowseLimit caps the page size of BaselineNodes.
pub const MAX_BROWSE_LIMIT: usize = 500;

/// NodeQuery selects a page of the nodes of a baseline.
#[derive(Debug, Clone, Default)]
pub struct NodeQuery {
    /// Type keeps the nodes of this qualified type (empty: every type).
    pub r#type: String,
    /// Text keeps the nodes whose key, type or a string property contains it (case-insensitive).
    pub text: String,
    /// IncludeDeleted keeps the tombstones.
    pub include_deleted: bool,
    pub offset: usize,
    pub limit: usize,
}

/// TypeCount is the number of nodes of a type in a baseline.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct TypeCount {
    pub r#type: String,
    pub count: usize,
}

/// NodePage is a page of the nodes of a baseline.
#[derive(Debug, Clone, Default)]
pub struct NodePage {
    pub nodes: Vec<Node>,
    /// Total is the number of nodes matching the query (all pages).
    pub total: usize,
    /// Types counts the nodes of each type matching the text filter (the type filter aside), sorted by type.
    pub types: Vec<TypeCount>,
}

/// LinkQuery selects a page of the links of a baseline.
#[derive(Debug, Clone, Default)]
pub struct LinkQuery {
    /// Type keeps the links of this type (empty: every type).
    pub r#type: String,
    /// Text keeps the links whose type or a string property contains it (case-insensitive).
    pub text: String,
    pub offset: usize,
    pub limit: usize,
}

/// LinkPage is a page of the links of a baseline.
#[derive(Debug, Clone, Default)]
pub struct LinkPage {
    pub links: Vec<Link>,
    /// Total is the number of links matching the query (all pages).
    pub total: usize,
    /// Types counts the links of each type matching the text filter (the type filter aside), sorted by type.
    pub types: Vec<TypeCount>,
}

/// Neighbourhood is a node of a baseline and the nodes it is linked to.
#[derive(Debug, Clone, Default)]
pub struct Neighbourhood {
    pub node: Node,
    /// Nodes are the neighbours: the targets of its outgoing links (at the baseline's version, or the linked one for
    /// a node the baseline does not hold) and the sources of its incoming links held by the baseline.
    pub nodes: Vec<Node>,
    /// Links are the links between Node and its neighbours.
    pub links: Vec<Link>,
    /// Suspect are the links whose target changed since they were created (the baseline holds another version).
    pub suspect: Vec<LinkID>,
}

fn counted(counts: BTreeMap<String, usize>) -> Vec<TypeCount> {
    counts
        .into_iter()
        .map(|(t, c)| TypeCount {
            r#type: t,
            count: c,
        })
        .collect()
}

fn page<T: Clone>(matched: &[T], offset: usize, limit: usize) -> Vec<T> {
    if offset < matched.len() {
        matched[offset..(offset + limit).min(matched.len())].to_vec()
    } else {
        Vec::new()
    }
}

impl Graph {
    /// BaselineNodes returns a page of the nodes of a baseline, ordered by key.
    pub async fn baseline_nodes(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
        mut q: NodeQuery,
    ) -> Result<NodePage> {
        if q.limit == 0 || q.limit > MAX_BROWSE_LIMIT {
            q.limit = MAX_BROWSE_LIMIT;
        }
        let text = q.text.trim().to_lowercase();
        let mut tx = self.begin(ctx).await?;
        let nodes = tx.nodes_in(ctx, id, "").await?;
        tx.commit(ctx).await?;
        let mut counts: BTreeMap<String, usize> = BTreeMap::new();
        let mut matched = Vec::new();
        for n in nodes {
            if (n.deleted && !q.include_deleted) || (!text.is_empty() && !node_matches(&n, &text)) {
                continue;
            }
            *counts.entry(n.r#type.clone()).or_default() += 1;
            if q.r#type.is_empty() || n.r#type == q.r#type {
                matched.push(n);
            }
        }
        Ok(NodePage {
            nodes: page(&matched, q.offset, q.limit),
            total: matched.len(),
            types: counted(counts),
        })
    }

    /// BaselineLinks returns a page of the links of a baseline (both ends held by it), ordered by type then id.
    pub async fn baseline_links(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
        mut q: LinkQuery,
    ) -> Result<LinkPage> {
        if q.limit == 0 || q.limit > MAX_BROWSE_LIMIT {
            q.limit = MAX_BROWSE_LIMIT;
        }
        let text = q.text.trim().to_lowercase();
        let mut tx = self.begin(ctx).await?;
        let b = tx.baseline(ctx, id).await?;
        let nodes = tx.nodes_in(ctx, id, "").await?;
        let mut links = links_within(ctx, &mut tx, &b, &nodes).await?;
        tx.commit(ctx).await?;
        links.sort_by(|a, b| a.r#type.cmp(&b.r#type).then_with(|| a.id.cmp(&b.id)));
        let mut counts: BTreeMap<String, usize> = BTreeMap::new();
        let mut matched = Vec::new();
        for l in links {
            if !text.is_empty() && !link_matches(&l, &text) {
                continue;
            }
            *counts.entry(l.r#type.clone()).or_default() += 1;
            if q.r#type.is_empty() || l.r#type == q.r#type {
                matched.push(l);
            }
        }
        Ok(LinkPage {
            links: page(&matched, q.offset, q.limit),
            total: matched.len(),
            types: counted(counts),
        })
    }

    /// NodeNeighbourhood returns a node of a baseline with its direct neighbours.
    pub async fn node_neighbourhood(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
        node_id: &NodeID,
    ) -> Result<Neighbourhood> {
        let mut tx = self.begin(ctx).await?;
        let b = tx.baseline(ctx, id).await?;
        let Some(v) = b.nodes.get(node_id).copied() else {
            return Err(Error::wrap(
                ErrorKind::NotFound,
                format!("node {node_id} in baseline {id}"),
            ));
        };
        let r = NodeRef {
            id: node_id.clone(),
            version: v,
        };
        let mut nb = Neighbourhood {
            node: tx.node(ctx, &r).await?,
            ..Default::default()
        };
        let mut seen: HashSet<NodeRef> = HashSet::from([r.clone()]);
        // Outgoing links belong to the source version: all of them are the node's.
        for l in tx.out_links(ctx, &r).await? {
            let mut to = l.to.clone();
            if let Some(tv) = b.nodes.get(&to.id)
                && *tv != to.version
            {
                to.version = *tv;
                nb.suspect.push(l.id.clone());
            }
            if seen.insert(to.clone()) {
                nb.nodes.push(tx.node(ctx, &to).await?);
            }
            nb.links.push(l);
        }
        // Incoming links may target any earlier version of the node (suspect links): keep those whose source is the
        // baseline's version.
        for ver in 1..=v {
            let ins = tx
                .in_links(
                    ctx,
                    &NodeRef {
                        id: node_id.clone(),
                        version: ver as Version,
                    },
                )
                .await?;
            for l in ins {
                if &l.from.id == node_id || !b.contains(&l.from) {
                    continue; // a self link is already among the outgoing ones
                }
                if seen.insert(l.from.clone()) {
                    nb.nodes.push(tx.node(ctx, &l.from).await?);
                }
                if ver != v {
                    nb.suspect.push(l.id.clone());
                }
                nb.links.push(l);
            }
        }
        tx.commit(ctx).await?;
        Ok(nb)
    }

    /// Namespaces returns the namespaces holding at least one node.
    pub async fn namespaces(&self, ctx: &Ctx) -> Result<Vec<String>> {
        let mut tx = self.begin(ctx).await?;
        let ns = tx.namespaces(ctx).await?;
        tx.commit(ctx).await?;
        Ok(ns)
    }
}

fn contains_text(props: &serde_json::Map<String, Value>, text: &str) -> bool {
    props
        .values()
        .any(|v| matches!(v, Value::String(s) if s.to_lowercase().contains(text)))
}

/// linkMatches reports whether the type or a string property of l contains text (lower case).
pub(crate) fn link_matches(l: &Link, text: &str) -> bool {
    l.r#type.to_lowercase().contains(text) || contains_text(&l.properties, text)
}

/// nodeMatches reports whether the key, the type or a string property of n contains text (lower case).
pub(crate) fn node_matches(n: &Node, text: &str) -> bool {
    n.key.to_lowercase().contains(text)
        || n.r#type.to_lowercase().contains(text)
        || contains_text(&n.properties, text)
}
