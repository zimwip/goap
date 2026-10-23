//! The verdicts of a submitted review (Go's `ImpactNodeReviewBatch`, ADR 0080, 0102, 0105): the record of the review is
//! written first, then every verdict as one entry of the log naming it as its cause; all of them stand or none does. An
//! acceptance is the gate of what a version must satisfy, checked on the draft: only a modification is accepted.
use std::collections::{BTreeMap, HashMap, HashSet};

use chrono::{DateTime, Utc};
use serde::Serialize;
use serde_json::{Map, Value};

use super::{check, tx::Tx};
use crate::{
    GraphError, Result,
    gojson::{self, time_text},
    impact::{
        ChangeImpact, Draft, DraftLink, INTENT_CREATED, ImpactEvent, ImpactOp, LOG_FACT, LogEntry, REVIEW_ACCEPTED,
        REVIEW_PROPOSED, REVIEW_REJECTED, Review,
    },
    model::{ChangeId, NodeRef, Props, Target},
    store::WriteTx,
};

/// The item type of the record of a review (`domain.TypeReview`).
const TYPE_REVIEW: &str = "review";
const RECORD_SUBMITTED: &str = "submitted";
const ITEM_PROPOSED: &str = "proposed";

/// A verdict of a batch: the impact, accepted or rejected, with its comment.
#[derive(Debug, Clone, Default)]
pub struct Verdict {
    pub impact: String,
    pub accept: bool,
    pub comment: String,
}

/// A submitted review: its key (allocated when empty), its flow, the execution that made it, its verdicts.
#[derive(Debug, Clone, Default)]
pub struct ReviewBatch {
    pub id: String,
    pub flow: String,
    pub execution: String,
    pub verdicts: Vec<Verdict>,
}

impl Verdict {
    fn status(&self) -> &'static str {
        if self.accept { REVIEW_ACCEPTED } else { REVIEW_REJECTED }
    }

    /// The outcome of the verdict in the record (`domain.ReviewOutcome`).
    fn outcome(&self) -> &'static str {
        if self.accept { "accept" } else { "reject" }
    }
}

/// The item recording a review, as Go marshals a `domain.ChangeItem` holding one.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Item<'a> {
    id: &'a str,
    #[serde(rename = "type")]
    typ: &'a str,
    status: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    flow: &'a str,
    data: &'a BTreeMap<String, Value>,
    #[serde(skip_serializing_if = "str::is_empty")]
    produced_by: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    execution: &'a str,
    #[serde(with = "gojson::time")]
    created_at: DateTime<Utc>,
}

/// The data of the record (`domain.ReviewRecord.Data`), keys sorted as Go marshals a map.
fn record_data(key: &str, flow: &str, by: &str, at: &DateTime<Utc>, verdicts: &[Verdict]) -> BTreeMap<String, Value> {
    let entries = verdicts
        .iter()
        .map(|v| {
            let mut m = Map::new();
            m.insert("impact".into(), Value::String(v.impact.clone()));
            let comment = v.comment.trim();
            if !comment.is_empty() {
                m.insert("comment".into(), Value::String(comment.into()));
            }
            m.insert("outcome".into(), Value::String(v.outcome().into()));
            Value::Object(m)
        })
        .collect();
    let mut d = BTreeMap::new();
    d.insert("key".into(), Value::String(key.into()));
    d.insert("status".into(), Value::String(RECORD_SUBMITTED.into()));
    d.insert("entries".into(), Value::Array(entries));
    if !flow.is_empty() {
        d.insert("flow".into(), Value::String(flow.into()));
    }
    if !by.is_empty() {
        d.insert("by".into(), Value::String(by.into()));
    }
    d.insert("submittedAt".into(), Value::String(time_text(at)));
    d
}

/// The verdict is accepted or rejected, with a comment (Go's `checkVerdict`).
fn check_verdict(v: &Verdict) -> Result<()> {
    if v.comment.trim().is_empty() {
        return Err(GraphError::invalid("a review needs a comment"));
    }
    Ok(())
}

fn unserved(what: &str) -> GraphError {
    GraphError::unimplemented(format!("{what}: not served here yet"))
}

/// The key of a link in a comparison of drafts (Go's `linkKey`): its type and its target.
fn link_key(l: &DraftLink) -> String {
    if l.is_external() {
        return format!("link:{}:{}", l.typ, l.target.logical());
    }
    format!("link:{}:{}", l.typ, l.to.id)
}

fn props_len(p: &Props) -> usize {
    p.as_ref().map_or(0, Map::len)
}

/// Two values the same once marshalled (Go's `sameValue`).
fn same(a: &Value, b: &Value) -> bool {
    gojson::to_string(a) == gojson::to_string(b)
}

fn same_props(a: &Props, b: &Props) -> bool {
    if props_len(a) == 0 && props_len(b) == 0 {
        return true;
    }
    let obj = |p: &Props| Value::Object(p.clone().unwrap_or_default());
    same(&obj(a), &obj(b))
}

/// Whether a draft differs from the version it was checked out from (Go's `DraftChanged`).
fn draft_changed(base: &Draft, d: &Draft) -> bool {
    if let (Some(m), Some(b)) = (&d.merged_with, &d.base)
        && m != b
    {
        return true;
    }
    if props_len(&base.props) != props_len(&d.props) {
        return true;
    }
    let empty = Map::new();
    let bp = base.props.as_ref().unwrap_or(&empty);
    for (k, v) in d.props.as_ref().unwrap_or(&empty) {
        match bp.get(k) {
            Some(b) if same(v, b) => {}
            _ => return true,
        }
    }
    if (!d.owner.is_zero() && d.owner != base.owner) || d.state != base.state {
        return true;
    }
    if base.links.len() != d.links.len() {
        return true;
    }
    let mut count: HashMap<String, usize> = HashMap::new();
    let mut first: HashMap<String, &DraftLink> = HashMap::new();
    for l in &base.links {
        let k = link_key(l);
        *count.entry(k.clone()).or_default() += 1;
        first.entry(k).or_insert(l);
    }
    for l in &d.links {
        let k = link_key(l);
        let n = count.get(&k).copied().unwrap_or(0);
        if n == 0 || !first.get(&k).is_some_and(|b| same_props(&l.props, &b.props)) {
            return true;
        }
        count.insert(k, n - 1);
    }
    false
}

impl<S: WriteTx> Tx<'_, S> {
    /// The version a draft was checked out from, as a draft (Go's `storedDraft`).
    async fn stored_draft(&self, r: &NodeRef) -> Result<Draft> {
        let n = self
            .store
            .node(r)
            .await?
            .ok_or_else(|| GraphError::not_found(format!("node version {r}")))?;
        let links = self
            .store
            .links_from(std::slice::from_ref(r), None)
            .await?
            .into_iter()
            .map(|l| {
                let (to, target) = match l.to {
                    Target::Node(t) => (t, Default::default()),
                    Target::External(u) => (NodeRef::default(), u),
                };
                DraftLink {
                    id: l.id,
                    typ: l.typ,
                    to,
                    target,
                    props: l.props,
                }
            })
            .collect();
        Ok(Draft {
            node: n.id.clone(),
            key: n.key.clone(),
            typ: n.typ.clone(),
            state: n.state.clone(),
            owner: n.owner.clone(),
            props: n.props.clone(),
            base: Some(r.clone()),
            links,
            ..Draft::default()
        })
    }

    /// What a draft must satisfy to be accepted, the version written from it to commit (Go's `checkDraft`): its
    /// properties, the links its type requires, the attributes of its links. A domain running scripts checks its
    /// validators in the Go service.
    fn check_draft(&self, ns: &str, d: &Draft) -> Result<()> {
        let cat = self.catalog();
        if cat.scripted(ns) {
            return Err(unserved(
                "the acceptance of a node of a domain running validators (ADR 0018)",
            ));
        }
        check::attributes(cat, &d.key, &d.typ, d.props.as_ref())?;
        let mut reqs: Vec<(String, i64, String)> = Vec::new();
        for st in cat.structures() {
            if d.typ == st.typ && d.key != st.root {
                reqs.push((st.parent.clone(), 1, format!("a parent {} ({})", st.typ, st.parent)));
            }
        }
        for r in cat.requires(&d.typ) {
            reqs.push((
                r.link.clone(),
                r.exactly(),
                format!("exactly {} {}", r.exactly(), r.link),
            ));
        }
        for (link, want, of) in reqs {
            let got = d.links.iter().filter(|l| l.typ == link).count() as i64;
            if got != want {
                return Err(GraphError::invalid(format!("{} needs {of}, got {got}", d.key)));
            }
        }
        for l in &d.links {
            check::link_attributes(cat, &l.typ, l.props.as_ref())
                .map_err(|e| e.with_prefix(&format!("{}: ", d.key)))?;
        }
        Ok(())
    }

    /// The gate of an acceptance (Go's `checkModifies` then `checkAcceptedIn`): the impact holds a draft that
    /// modifies its node, and the draft is what a version must be.
    async fn check_accepted(&self, ns: &str, change: &ChangeId, cn: &ChangeImpact) -> Result<()> {
        let d = self.views[change].fold.draft(&cn.id, "").cloned();
        let Some(d) = d else {
            return Err(GraphError::invalid(format!(
                "change impact {} ({}) modifies nothing: check the node out and change it, or withdraw the impact",
                cn.key, cn.id
            )));
        };
        if let Some(b) = &d.base
            && !draft_changed(&self.stored_draft(b).await?, &d)
        {
            return Err(GraphError::invalid(format!(
                "change impact {} ({}) modifies nothing: its draft is the version it was checked out from",
                cn.key, cn.id
            )));
        }
        if cn.post.is_none() {
            return Ok(());
        }
        if cn.intent == INTENT_CREATED && !d.origins.is_empty() {
            return Err(unserved(
                "the acceptance of the successor of a merge or a split (ADR 0077)",
            ));
        }
        self.check_draft(ns, &d)
    }

    /// Submits a review on the main flow of a change (Go's `ImpactNodeReviewBatch` with no item of the caller's): the
    /// record, then the verdicts as one entry. It answers the impacts as the verdicts leave them, in their order.
    pub async fn impact_node_review_batch(&mut self, id: &ChangeId, b: ReviewBatch) -> Result<Vec<ChangeImpact>> {
        if !b.flow.is_empty() {
            return Err(unserved("a review on an option of a change (ADR 0032)"));
        }
        if b.verdicts.is_empty() {
            return Err(GraphError::invalid("a review batch needs at least one verdict"));
        }
        let many = b.verdicts.len() > 1;
        let label = |n: usize, what: &str| {
            if many {
                format!("verdict {} ({what}): ", n + 1)
            } else {
                String::new()
            }
        };
        let mut seen = HashSet::new();
        for (n, v) in b.verdicts.iter().enumerate() {
            check_verdict(v).map_err(|e| e.with_prefix(&label(n, &v.impact)))?;
            if !seen.insert(v.impact.as_str()) {
                return Err(GraphError::invalid(format!(
                    "verdict {}: change impact {} is reviewed twice in the batch",
                    n + 1,
                    v.impact
                )));
            }
        }
        let by = self.env.by.clone();
        let key = if b.id.is_empty() {
            format!("REV-{}", self.env.new_id())
        } else {
            b.id.clone()
        };
        let item = self.env.new_id();
        let at = self.env.now();
        let data = record_data(&key, &b.flow, &by, &at, &b.verdicts);
        let payload = gojson::to_string(&Item {
            id: &item,
            typ: TYPE_REVIEW,
            status: ITEM_PROPOSED,
            flow: "",
            data: &data,
            produced_by: &by,
            execution: &b.execution,
            created_at: at,
        });

        self.lock_change(id).await?;
        self.change_head(id).await?;
        self.append_log(&LogEntry {
            seq: 0,
            id: item.clone(),
            change: id.clone(),
            typ: format!("{LOG_FACT}.{TYPE_REVIEW}"),
            flow: String::new(),
            process: String::new(),
            execution: b.execution.clone(),
            subject: String::new(),
            by: by.clone(),
            at,
            causation: String::new(),
            payload,
        })
        .await?;
        let w = self.work_on(id, "").await?;
        let mut out = Vec::with_capacity(b.verdicts.len());
        let mut events = Vec::with_capacity(b.verdicts.len());
        for (n, v) in b.verdicts.iter().enumerate() {
            let named = |e: GraphError| {
                let what = self.views[id]
                    .fold
                    .impact(&v.impact)
                    .map_or(v.impact.clone(), |cn| cn.key.clone());
                e.with_prefix(&label(n, &what))
            };
            let Some(mut cn) = self.views[id].fold.impact(&v.impact).cloned() else {
                return Err(named(GraphError::not_found(format!(
                    "change impact {} not in change {id}",
                    v.impact
                ))));
            };
            let Some(vcn) = w.seen.iter().find(|c| c.id == v.impact) else {
                return Err(named(GraphError::not_found(format!(
                    "change impact {} is not on the flow \"\" of change {id}",
                    v.impact
                ))));
            };
            if vcn.review != REVIEW_PROPOSED {
                return Err(named(GraphError::conflict(format!(
                    "change impact {} is already {}",
                    v.impact, vcn.review
                ))));
            }
            if v.accept {
                self.check_accepted(&w.ns, id, vcn).await.map_err(named)?;
            }
            let r = Review {
                status: v.status().into(),
                by: by.clone(),
                comment: v.comment.trim().into(),
                at: self.env.now(),
                execution: b.execution.clone(),
                review_id: key.clone(),
                ..Review::default()
            };
            cn.review = r.status.clone();
            cn.reviews.push(r.clone());
            events.push(ImpactEvent {
                change: id.clone(),
                impact: cn.id.clone(),
                op: ImpactOp::Reviewed,
                execution: b.execution.clone(),
                by: by.clone(),
                review: Some(r),
                causation: item.clone(),
                ..ImpactEvent::default()
            });
            out.push(cn);
        }
        self.emit_staged(events).await?;
        Ok(out)
    }
}
