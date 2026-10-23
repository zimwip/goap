//! The changes as their headers say them (what a list shows, ADR 0140), and the reads over them: the pages of changes,
//! the changes acting on a node, the lineage between two baselines and the difference of their states (ADR 0133,
//! 0140). The impacts, items and drafts of a change are a fold of its log (ADR 0153): not read here.
use std::{
    collections::{HashMap, HashSet},
    future::Future,
    pin::Pin,
};

use chrono::{DateTime, SecondsFormat, Utc};

use crate::{
    GraphError, Result,
    lnk::Url,
    model::{BaselineId, ChangeId, Node, NodeId, NodeRef, Props, Version, namespace_of},
    read::{header_of, select},
    store::ReadTx,
};

/// The status of a change merged into the branch it forked from.
pub const STATUS_APPLIED: &str = "applied";

/// A methodology of the guardian chain of a change (ADR 0106).
#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub struct GuardianRef {
    #[serde(default)]
    pub methodology: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub version: String,
}

/// The header of a change: the change without its impacts and items.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Change {
    pub id: ChangeId,
    pub title: String,
    pub intent: String,
    pub methodology: String,
    pub goal: String,
    pub status: String,
    pub baseline_id: BaselineId,
    pub result_baseline_id: BaselineId,
    pub data: Props,
    pub created_at: Option<DateTime<Utc>>,
    pub branch: String,
    pub namespace: String,
    pub parent_id: ChangeId,
    pub owner_org: Url,
    pub project_id: Url,
    pub lifecycle: String,
    pub state: String,
    pub guardians: Vec<GuardianRef>,
    pub depends_on: Vec<ChangeId>,
    pub methodology_version: String,
}

/// The prefix of the key of a user node, which is also its personal unit (ADR 0037).
pub const USER_PREFIX: &str = "USR:";

impl Change {
    /// Whether a personal unit holds the change: only its subject reads it.
    pub fn is_personal(&self) -> bool {
        self.owner_org.key.starts_with(USER_PREFIX)
    }

    /// Whether the change is personal to a subject.
    pub fn is_personal_to(&self, subject: &str) -> bool {
        !subject.is_empty() && self.is_personal() && self.owner_org.key[USER_PREFIX.len()..] == *subject
    }

    /// Whether a caller sees the change at all: any change but the personal changes of others.
    pub fn visible_to(&self, subject: &str) -> bool {
        !self.is_personal() || self.is_personal_to(subject)
    }
}

/// What narrows a listing of changes; the default matches every change. The unit and the project are the URLs the
/// changes store (the caller resolves a key).
#[derive(Debug, Clone, Default)]
pub struct ChangesFilter {
    pub namespace: String,
    pub owner_org: String,
    pub status: Vec<String>,
    pub project: String,
    /// A text the id, title, intent, methodology or namespace contains, case ignored.
    pub query: String,
    pub parent: ChangeId,
    pub depends_on: ChangeId,
    pub ids: Vec<ChangeId>,
}

impl ChangesFilter {
    /// Whether a change satisfies the filter (`graphapi.ChangesFilter.Match`).
    pub fn matches(&self, c: &Change) -> bool {
        if !self.namespace.is_empty() && c.namespace != self.namespace {
            return false;
        }
        if !self.owner_org.is_empty() && !c.owner_org.names(&self.owner_org) {
            return false;
        }
        if !self.status.is_empty() && !self.status.contains(&c.status) {
            return false;
        }
        if !self.project.is_empty() && !c.project_id.names(&self.project) {
            return false;
        }
        if !self.parent.is_empty() && c.parent_id != self.parent {
            return false;
        }
        if !self.depends_on.is_empty() && !c.depends_on.contains(&self.depends_on) {
            return false;
        }
        if !self.ids.is_empty() && !self.ids.contains(&c.id) {
            return false;
        }
        let q = self.query.trim().to_lowercase();
        if !q.is_empty() {
            let text = [c.id.as_str(), &c.title, &c.intent, &c.methodology, &c.namespace]
                .join(" ")
                .to_lowercase();
            if !text.contains(&q) {
                return false;
            }
        }
        true
    }
}

/// A page of changes, newest first.
#[derive(Debug, Clone, Default)]
pub struct ChangePage {
    pub changes: Vec<Change>,
    /// The cursor of the next (older) page; empty: this page is the last.
    pub next: String,
    /// The changes matched, every page together.
    pub total: usize,
}

/// The size of a page of a history: 50 by default, at most 500 (Go's `pageLimit`).
pub fn page_limit(limit: i64) -> usize {
    if limit <= 0 { 50 } else { limit.min(500) as usize }
}

/// A time as Go's RFC 3339 with nanoseconds writes it: UTC, the trailing zeros of the fraction left out.
fn rfc3339_nano(t: &DateTime<Utc>) -> String {
    let s = t.to_rfc3339_opts(SecondsFormat::Nanos, true);
    let (head, frac) = s.trim_end_matches('Z').split_once('.').unwrap_or((&s, ""));
    let frac = frac.trim_end_matches('0');
    if frac.is_empty() {
        format!("{}Z", head.trim_end_matches('Z'))
    } else {
        format!("{head}.{frac}Z")
    }
}

/// The cursor of the page after a change: its creation time and its id.
fn change_cursor(c: &Change) -> String {
    let at = c
        .created_at
        .map(|t| rfc3339_nano(&t))
        .unwrap_or_else(|| "0001-01-01T00:00:00Z".into());
    format!("{at}|{}", c.id)
}

fn parse_change_cursor(s: &str) -> Option<(Option<DateTime<Utc>>, String)> {
    let (at, id) = s.split_once('|')?;
    let t = DateTime::parse_from_rfc3339(at).ok()?.with_timezone(&Utc);
    // Go's zero time is no time
    let t = (t.timestamp() != -62135596800).then_some(t);
    Some((t, id.to_string()))
}

/// A page of the headers of the changes a filter matches, newest first (creation time, then id), of at most `limit`
/// changes after `cursor` (the `next` of the page before; empty: the newest page). `keep` leaves out the changes the
/// caller may not see before the page is cut, so a page is full whenever more remain.
pub async fn changes_page<T: ReadTx>(
    tx: &T,
    f: &ChangesFilter,
    limit: usize,
    cursor: &str,
    keep: impl Fn(&Change) -> bool,
) -> Result<ChangePage> {
    let mut matched: Vec<Change> = tx
        .change_heads(f)
        .await?
        .into_iter()
        .filter(|c| f.matches(c) && keep(c))
        .collect();
    matched.sort_by(|a, b| b.created_at.cmp(&a.created_at).then_with(|| b.id.cmp(&a.id)));
    let total = matched.len();
    let start = match parse_change_cursor(cursor) {
        // the changes up to the cursor, itself included, are before it
        Some((at, id)) => {
            matched.partition_point(|c| c.created_at > at || (c.created_at == at && c.id.as_str() >= id.as_str()))
        }
        None => 0,
    };
    let end = (start + limit).min(total);
    let page: Vec<Change> = matched.drain(start..end).collect();
    let next = if start + limit < total {
        page.last().map(change_cursor).unwrap_or_default()
    } else {
        String::new()
    };
    Ok(ChangePage {
        changes: page,
        next,
        total,
    })
}

/// The headers of some changes in one read; a change the store does not hold is absent.
pub async fn change_heads<T: ReadTx>(tx: &T, ids: &[ChangeId]) -> Result<HashMap<ChangeId, Change>> {
    if ids.is_empty() {
        return Ok(HashMap::new());
    }
    let mut want = ids.to_vec();
    want.sort();
    want.dedup();
    let f = ChangesFilter {
        ids: want,
        ..ChangesFilter::default()
    };
    Ok(tx
        .change_heads(&f)
        .await?
        .into_iter()
        .map(|c| (c.id.clone(), c))
        .collect())
}

/// A page of the changes acting on a node (ADR 0140): the last `limit` whose cursor is below `before` (0: up to the
/// latest), oldest first, headers only, and the `before` of the next older page (0: none).
pub async fn node_changes<T: ReadTx>(tx: &T, node: &NodeId, before: i64, limit: usize) -> Result<(Vec<Change>, i64)> {
    let mut page = tx.node_change_page(node, before, limit + 1).await?;
    let mut next = 0;
    if page.len() > limit {
        page.truncate(limit);
        next = page.last().map(|p| p.1).unwrap_or_default();
    }
    let ids: Vec<ChangeId> = page.iter().map(|p| p.0.clone()).collect();
    let mut heads = change_heads(tx, &ids).await?;
    let mut out = Vec::with_capacity(page.len());
    for (id, _) in page.iter().rev() {
        match heads.remove(id) {
            Some(c) => out.push(c),
            None => return Err(GraphError::not_found(format!("change {id}"))),
        }
    }
    Ok((out, next))
}

/// A change of a change-stack: what a lineage names of it (ADR 0133).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct StackChange {
    pub id: ChangeId,
    pub parent: ChangeId,
    pub title: String,
    pub status: String,
    pub owner_org: Url,
}

/// The changes of a stack; a change the graph no longer holds (purged) keeps its id alone.
pub async fn stack_changes<T: ReadTx>(tx: &T, ids: &[ChangeId]) -> Result<Vec<StackChange>> {
    let heads = change_heads(tx, ids).await?;
    Ok(ids
        .iter()
        .map(|id| match heads.get(id) {
            Some(c) => StackChange {
                id: id.clone(),
                parent: c.parent_id.clone(),
                title: c.title.clone(),
                status: c.status.clone(),
                owner_org: c.owner_org.clone(),
            },
            None => StackChange {
                id: id.clone(),
                ..StackChange::default()
            },
        })
        .collect())
}

/// The most changes between two baselines a difference walks: further apart, it is refused.
pub const MAX_DIFF_CHANGES: usize = 1000;

/// What lies between two baselines: the changes the stack of the second holds and the first does not (`added`), the
/// other way round (`removed`), and every change of a baseline only one of them reaches (`changes`), whose nodes are
/// all the two states may differ on.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Gap {
    pub added: Vec<ChangeId>,
    pub removed: Vec<ChangeId>,
    pub changes: Vec<ChangeId>,
}

const FROM: u8 = 1;
const TO: u8 = 2;
const BOTH: u8 = 3;

/// The walk of two lineages: each baseline painted with the side(s) reaching it, the ones left to walk by order.
struct Walk<'a, T> {
    tx: &'a T,
    paint: HashMap<BaselineId, u8>,
    headers: HashMap<BaselineId, crate::model::Baseline>,
    /// Left to walk, by order, the newest last.
    todo: Vec<BaselineId>,
    namespace: String,
}

impl<T: ReadTx> Walk<'_, T> {
    async fn reach(&mut self, id: &BaselineId, side: u8) -> Result<()> {
        let painted = self.paint.get(id).copied().unwrap_or(0);
        if id.is_empty() || painted | side == painted {
            return Ok(());
        }
        if !self.headers.contains_key(id) {
            let b = header_of(self.tx, id).await?;
            let ns = namespace_of(&b.namespace).to_string();
            if !self.namespace.is_empty() && ns != self.namespace {
                return Err(GraphError::invalid(format!(
                    "baseline {id} is not of namespace {}",
                    self.namespace
                )));
            }
            self.namespace = ns;
            self.headers.insert(id.clone(), b);
        }
        if painted == 0 {
            let seq = self.headers[id].seq;
            let i = self.todo.partition_point(|x| self.headers[x].seq < seq);
            self.todo.insert(i, id.clone());
        }
        *self.paint.entry(id.clone()).or_default() |= side;
        Ok(())
    }
}

/// The lineages of two baselines (parent, merged head) walked newest first by order down to where they meet (ADR
/// 0140): the walk follows the distance between the baselines, never the length of the history.
pub async fn lineage_gap<T: ReadTx>(tx: &T, from: &BaselineId, to: &BaselineId) -> Result<Gap> {
    let mut w = Walk {
        tx,
        paint: HashMap::new(),
        headers: HashMap::new(),
        todo: Vec::new(),
        namespace: String::new(),
    };
    w.reach(from, FROM).await?;
    w.reach(to, TO).await?;
    let mut seen: Vec<(crate::model::Baseline, u8)> = Vec::new();
    while !w.todo.is_empty() && w.todo.iter().any(|id| w.paint[id] != BOTH) {
        if seen.len() >= MAX_DIFF_CHANGES {
            return Err(GraphError::invalid(format!(
                "baselines {from} and {to} are more than {MAX_DIFF_CHANGES} changes apart: compare closer baselines"
            )));
        }
        let id = w.todo.pop().expect("not empty");
        let (b, side) = (w.headers[&id].clone(), w.paint[&id]);
        for next in [&b.parent_id, &b.merged_from] {
            w.reach(next, side).await?;
        }
        seen.push((b, side));
    }
    // oldest first, as a stack reads
    seen.sort_by_key(|x| x.0.created_at);
    // every baseline painted counts, the ones left to walk too: a change a side reaches is in its stack
    let mut ins: [HashSet<ChangeId>; 2] = Default::default();
    for (id, side) in &w.paint {
        let c = &w.headers[id].change_id;
        for (i, s) in [FROM, TO].into_iter().enumerate() {
            if side & s != 0 && !c.is_empty() {
                ins[i].insert(c.clone());
            }
        }
    }
    let mut g = Gap::default();
    for (b, side) in &seen {
        let c = &b.change_id;
        if c.is_empty() || *side == BOTH {
            continue;
        }
        if !g.changes.contains(c) {
            g.changes.push(c.clone());
        }
        if *side == TO && !ins[0].contains(c) && !g.added.contains(c) {
            g.added.push(c.clone());
        } else if *side == FROM && !ins[1].contains(c) && !g.removed.contains(c) {
            g.removed.push(c.clone());
        }
    }
    // the sub-changes merged into a change are in every stack it is in (ADR 0103): the branch of a change holds no
    // baseline (ADR 0140), the stacks name them through their parent
    let mut merged = MergedInto {
        tx,
        cache: HashMap::new(),
    };
    g.added = merged.expand(&g.added).await;
    g.removed = merged.expand(&g.removed).await;
    g.changes = merged.expand(&g.changes).await;
    Ok(g)
}

/// The sub-changes merged into a change, recursively, oldest first; a change the graph no longer holds has none.
struct MergedInto<'a, T> {
    tx: &'a T,
    cache: HashMap<ChangeId, Vec<ChangeId>>,
}

impl<T: ReadTx> MergedInto<'_, T> {
    fn walk<'s>(
        &'s mut self,
        id: &'s ChangeId,
        depth: usize,
    ) -> Pin<Box<dyn Future<Output = Vec<ChangeId>> + Send + 's>>
    where
        T: Sync,
    {
        Box::pin(async move {
            if let Some(out) = self.cache.get(id) {
                return out.clone();
            }
            if depth > 64 {
                return Vec::new();
            }
            let f = ChangesFilter {
                parent: id.clone(),
                ..ChangesFilter::default()
            };
            // the headers alone: their status and order are what counts
            let Ok(mut subs) = self.tx.change_heads(&f).await else {
                return Vec::new();
            };
            subs.sort_by_key(|c| c.created_at);
            let mut out = Vec::new();
            for sc in subs.into_iter().filter(|c| c.status == STATUS_APPLIED) {
                out.extend(self.walk(&sc.id, depth + 1).await);
                out.push(sc.id);
            }
            self.cache.insert(id.clone(), out.clone());
            out
        })
    }

    async fn expand(&mut self, ids: &[ChangeId]) -> Vec<ChangeId> {
        let mut out = Vec::new();
        for id in ids {
            for m in self.walk(id, 0).await {
                if !out.contains(&m) && !ids.contains(&m) {
                    out.push(m);
                }
            }
            out.push(id.clone());
        }
        out
    }
}

/// The kinds of a difference of two baselines.
pub const DIFF_ADDED: &str = "added";
pub const DIFF_REMOVED: &str = "removed";
pub const DIFF_CHANGED: &str = "changed";

/// A node whose version differs between two baselines: `from` / `to` the node as each holds it.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct BaselineDiff {
    pub node: NodeId,
    pub key: String,
    pub typ: String,
    pub kind: &'static str,
    pub from: Option<Node>,
    pub to: Option<Node>,
}

/// The difference of two baselines of a namespace, node by node, ordered by key (ADR 0140): the changes only one of
/// them takes in name the nodes the states may differ on (those of their impacts and of their own branches); those
/// nodes alone are read in each baseline.
pub async fn diff_baselines<T: ReadTx>(tx: &T, from: &BaselineId, to: &BaselineId) -> Result<(Vec<BaselineDiff>, Gap)> {
    let gap = lineage_gap(tx, from, to).await?;
    let mut ids: Vec<NodeId> = Vec::new();
    let mut seen = HashSet::new();
    let mut add = |id: NodeId, ids: &mut Vec<NodeId>| {
        if !id.is_empty() && seen.insert(id.clone()) {
            ids.push(id);
        }
    };
    let heads = change_heads(tx, &gap.changes).await?;
    for id in &gap.changes {
        let Some(c) = heads.get(id) else { continue };
        for r in tx.impact_refs(id).await? {
            add(r.id, &mut ids);
        }
        if !c.branch.is_empty() {
            // what its branch holds: a node it reverted keeps no impact
            for n in tx.branch_node_ids(&c.branch).await? {
                add(n, &mut ids);
            }
        }
    }
    let a = versions_at(tx, from, &ids).await?;
    let b = versions_at(tx, to, &ids).await?;
    Ok((diff_states(tx, &a, &b).await?, gap))
}

async fn versions_at<T: ReadTx>(tx: &T, at: &BaselineId, ids: &[NodeId]) -> Result<HashMap<NodeId, Version>> {
    if at.is_empty() || ids.is_empty() {
        return Ok(HashMap::new());
    }
    let h = header_of(tx, at).await?;
    select(tx, &h, Some(ids)).await
}

/// Two states over the same nodes compared, the versions of both sides read in one statement.
async fn diff_states<T: ReadTx>(
    tx: &T,
    a: &HashMap<NodeId, Version>,
    b: &HashMap<NodeId, Version>,
) -> Result<Vec<BaselineDiff>> {
    let mut refs = Vec::new();
    for (id, va) in a {
        let vb = b.get(id);
        if vb != Some(va) {
            refs.push(NodeRef::new(id.clone(), *va));
            if let Some(vb) = vb {
                refs.push(NodeRef::new(id.clone(), *vb));
            }
        }
    }
    for (id, vb) in b {
        if !a.contains_key(id) {
            refs.push(NodeRef::new(id.clone(), *vb));
        }
    }
    let mut nodes: HashMap<NodeRef, Node> = tx
        .nodes_at(&refs, "")
        .await?
        .into_iter()
        .map(|n| (NodeRef::new(n.id.clone(), n.version), n))
        .collect();
    let mut node = |id: &NodeId, v: Version| {
        nodes
            .remove(&NodeRef::new(id.clone(), v))
            .ok_or_else(|| GraphError::not_found(format!("node {id} v{v}")))
    };
    let mut out = Vec::new();
    for (id, va) in a {
        let vb = b.get(id);
        if vb == Some(va) {
            continue;
        }
        let mut d = BaselineDiff {
            node: id.clone(),
            kind: DIFF_REMOVED,
            from: Some(node(id, *va)?),
            ..BaselineDiff::default()
        };
        if let Some(vb) = vb {
            d.kind = DIFF_CHANGED;
            d.to = Some(node(id, *vb)?);
        }
        out.push(d);
    }
    for (id, vb) in b {
        if !a.contains_key(id) {
            out.push(BaselineDiff {
                node: id.clone(),
                kind: DIFF_ADDED,
                to: Some(node(id, *vb)?),
                ..BaselineDiff::default()
            });
        }
    }
    for d in &mut out {
        let n = d.to.as_ref().or(d.from.as_ref()).expect("one side holds it");
        (d.key, d.typ) = (n.key.clone(), n.typ.clone());
    }
    out.sort_by(|x, y| x.key.cmp(&y.key));
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_cursor_writes_its_time_as_go_does() {
        let t = DateTime::parse_from_rfc3339("2026-10-23T10:00:00.120000Z")
            .unwrap()
            .with_timezone(&Utc);
        assert_eq!(rfc3339_nano(&t), "2026-10-23T10:00:00.12Z");
        let t = DateTime::parse_from_rfc3339("2026-10-23T10:00:00Z")
            .unwrap()
            .with_timezone(&Utc);
        assert_eq!(rfc3339_nano(&t), "2026-10-23T10:00:00Z");
        let c = Change {
            id: "c1".into(),
            created_at: Some(t),
            ..Change::default()
        };
        let (at, id) = parse_change_cursor(&change_cursor(&c)).unwrap();
        assert_eq!((at, id.as_str()), (Some(t), "c1"));
    }

    #[test]
    fn a_filter_matches_as_go_matches() {
        let c = Change {
            id: "abc".into(),
            title: "Fix the Login".into(),
            status: "active".into(),
            owner_org: Url::new("organisation", "OrgUnit", "ORG-DEFAULT"),
            ..Change::default()
        };
        let f = |q: &str| ChangesFilter {
            query: q.into(),
            ..ChangesFilter::default()
        };
        assert!(f(" login ").matches(&c));
        assert!(!f("logout").matches(&c));
        let by_unit = ChangesFilter {
            owner_org: "lnk:organisation/OrgUnit/ORG-DEFAULT".into(),
            status: vec!["active".into()],
            ..ChangesFilter::default()
        };
        assert!(by_unit.matches(&c));
        assert!(!c.is_personal());
        let mine = Change {
            owner_org: Url::new("organisation", "User", "USR:ann"),
            ..Change::default()
        };
        assert!(mine.visible_to("ann") && !mine.visible_to("bob"));
    }
}
