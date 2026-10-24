//! The verdicts of a submitted review (Go's `ImpactNodeReviewBatch`, ADR 0080, 0102, 0105): the record of the review is
//! written first, then every verdict as one entry of the log naming it as its cause; all of them stand or none does. An
//! acceptance is the gate of what a version must satisfy, checked on the draft: only a modification is accepted.
use std::borrow::Borrow;
use std::collections::{BTreeMap, HashMap, HashSet};

use chrono::{DateTime, Utc};
use serde::Serialize;
use serde_json::{Map, Value};

use super::{check, scripts, tx::Tx};
use crate::{
    GraphError, Result,
    gojson::{self, time_text},
    impact::{
        ChangeImpact, Draft, DraftLink, INTENT_CREATED, ImpactEvent, ImpactOp, LOG_FACT, LogEntry, REVIEW_ACCEPTED,
        REVIEW_PROPOSED, REVIEW_REJECTED, Review,
    },
    model::{ChangeId, NodeRef, Props, Target},
    store::{ReadTx, WriteTx},
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
    /// the data of the submitted record the use case built (`domain.ReviewBatch.Item`), checked against the verdicts;
    /// none: the graph makes it of them
    pub record: Option<Map<String, Value>>,
    /// the type of the item given with the record (a review)
    pub record_type: String,
    /// who reviews (`domain.ReviewBatch.By`, a commit's producer); empty: the caller
    pub by: String,
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

/// The record of a batch the use case built (Go's `reviewRecord` with an item): submitted, of the batch's key, recording
/// each verdict. It answers its key.
fn check_record(b: &ReviewBatch, rec: &Map<String, Value>) -> Result<String> {
    let d = Some(rec);
    let key = super::items::data_text(d, "key").to_string();
    let status = super::items::data_text(d, "status");
    if b.record_type != TYPE_REVIEW || status != RECORD_SUBMITTED {
        return Err(GraphError::invalid(format!(
            "the item of a review batch is the submitted review, not a {status} {} item",
            b.record_type
        )));
    }
    if !b.id.is_empty() && key != b.id {
        return Err(GraphError::invalid(format!(
            "review batch {} carries the record of review {key}",
            b.id
        )));
    }
    let entries = super::items::review_entries(d);
    if entries.len() != b.verdicts.len() {
        return Err(GraphError::invalid(format!(
            "review {key} has {} entries for {} verdicts",
            entries.len(),
            b.verdicts.len()
        )));
    }
    for v in &b.verdicts {
        if !entries.iter().any(|(i, _, o)| *i == v.impact && o == v.outcome()) {
            return Err(GraphError::invalid(format!(
                "review {key} does not record the verdict on {}",
                v.impact
            )));
        }
    }
    Ok(key)
}

/// The verdict is accepted or rejected, with a comment (Go's `checkVerdict`).
fn check_verdict(v: &Verdict) -> Result<()> {
    if v.comment.trim().is_empty() {
        return Err(GraphError::invalid("a review needs a comment"));
    }
    Ok(())
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
pub(crate) fn draft_changed(base: &Draft, d: &Draft) -> bool {
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

/// The version a draft was checked out from, as a draft (Go's `storedDraft`).
pub(crate) async fn stored_draft<T: ReadTx>(store: &T, r: &NodeRef) -> Result<Draft> {
    let n = store
        .node(r)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("node version {r}")))?;
    let links = store
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

impl<S: WriteTx> Tx<'_, S> {
    /// The version a draft was checked out from, as a draft (Go's `storedDraft`).
    pub(super) async fn stored_draft(&self, r: &NodeRef) -> Result<Draft> {
        stored_draft(self.store, r).await
    }

    /// What a draft must satisfy to be accepted, the version written from it to commit (Go's `checkDraft`): its
    /// properties, the links its type requires, the attributes of its links, the validators of its type and of its
    /// links (their targets read through the drafts the flow sees).
    async fn check_draft(&self, change: &ChangeId, flow: &str, d: &Draft) -> Result<()> {
        let drafts = self.views[change].seen_drafts(flow);
        let to = scripts::draft_targets(self.store, self.catalog(), &d.links, &drafts).await?;
        let links: Vec<_> = d
            .links
            .iter()
            .zip(to)
            .map(|(l, to)| (l.typ.as_str(), l.props.as_ref(), to))
            .collect();
        check::version(self.catalog(), &scripts::draft_node(d), &links)
    }

    /// The gate of an acceptance (Go's `checkModifies` then `checkAcceptedIn`): the impact holds a draft that
    /// modifies its node, and the draft is what a version must be.
    async fn check_accepted(
        &self,
        change: &ChangeId,
        flow: &str,
        seen: &[ChangeImpact],
        cn: &ChangeImpact,
    ) -> Result<()> {
        // the conflicts a rebase left are settled first (Go's `checkSettled`)
        if let Some(pending) = self.views[change].fold.conflicts.get(&cn.id) {
            return Err(GraphError::conflict(format!(
                "change impact {} has conflicts with the branch it was forked from to settle first (edit the fields, or keep them with ImpactNodeResolve): {}",
                cn.id,
                pending.join(", ")
            )));
        }
        let d = self.views[change].seen_draft(flow, &cn.id);
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
        check_origins(seen, cn, &d)?;
        self.check_draft(change, flow, &d).await
    }

    /// Records a review the graph submits itself (Go's `putReviewRecord`): the merge of a sub-change accepting what it
    /// committed. It answers the key of the record and the id of its item, the cause of its verdicts.
    pub(crate) async fn put_review_record(
        &mut self,
        change: &ChangeId,
        by: &str,
        comment: &str,
        entries: &[(String, String)],
    ) -> Result<(String, String)> {
        let key = format!("REV-{}", self.env.new_id());
        let at = self.env.now();
        let entries = entries
            .iter()
            .map(|(impact, comment)| {
                let mut m = Map::new();
                m.insert("impact".into(), Value::String(impact.clone()));
                if !comment.is_empty() {
                    m.insert("comment".into(), Value::String(comment.clone()));
                }
                m.insert("outcome".into(), Value::String("accept".into()));
                Value::Object(m)
            })
            .collect();
        let mut data = BTreeMap::new();
        data.insert("key".to_string(), Value::String(key.clone()));
        data.insert("status".into(), Value::String(RECORD_SUBMITTED.into()));
        data.insert("entries".into(), Value::Array(entries));
        if !comment.is_empty() {
            data.insert("comment".into(), Value::String(comment.into()));
        }
        if !by.is_empty() {
            data.insert("by".into(), Value::String(by.into()));
        }
        data.insert("submittedAt".into(), Value::String(time_text(&at)));
        let item = self.env.new_id();
        let payload = gojson::to_string(&Item {
            id: &item,
            typ: TYPE_REVIEW,
            status: ITEM_PROPOSED,
            flow: "",
            data: &data,
            produced_by: by,
            execution: "",
            created_at: at,
        });
        self.append_log(&LogEntry {
            seq: 0,
            id: item.clone(),
            change: change.clone(),
            typ: format!("{LOG_FACT}.{TYPE_REVIEW}"),
            flow: String::new(),
            process: String::new(),
            execution: String::new(),
            subject: String::new(),
            by: by.to_string(),
            at,
            causation: String::new(),
            payload,
        })
        .await?;
        Ok((key, item))
    }

    /// Submits a review of a change (Go's `ImpactNodeReviewBatch` with no item of the caller's): the record, then the
    /// verdicts, as one entry on the main flow, one each on a flow. It answers the impacts as the verdicts leave them,
    /// in their order.
    pub async fn impact_node_review_batch(&mut self, id: &ChangeId, b: ReviewBatch) -> Result<Vec<ChangeImpact>> {
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
        let by = if b.by.is_empty() {
            self.env.by.clone()
        } else {
            b.by.clone()
        };
        let at = self.env.now();
        let (key, data) = match &b.record {
            Some(rec) => (check_record(&b, rec)?, rec.clone().into_iter().collect()),
            None => {
                let key = if b.id.is_empty() {
                    format!("REV-{}", self.env.new_id())
                } else {
                    b.id.clone()
                };
                let data = record_data(&key, &b.flow, &by, &at, &b.verdicts);
                (key, data)
            }
        };
        let item = self.env.new_id();
        self.lock_change(id).await?;
        self.change_head(id).await?;
        self.view(id).await?;
        // the verdicts are made on this flow (ADR 0032 §6)
        let flow = self.views[id].flows.resolve(&b.flow);
        super::items::validate_review(Some(&data.clone().into_iter().collect()))
            .map_err(|e| GraphError::invalid(format!("item {item}: {e}")))?;
        let payload = gojson::to_string(&Item {
            id: &item,
            typ: TYPE_REVIEW,
            status: ITEM_PROPOSED,
            flow: &flow,
            data: &data,
            produced_by: &by,
            execution: &b.execution,
            created_at: at,
        });

        self.append_log(&LogEntry {
            seq: 0,
            id: item.clone(),
            change: id.clone(),
            typ: format!("{LOG_FACT}.{TYPE_REVIEW}"),
            flow: flow.clone(),
            process: String::new(),
            execution: b.execution.clone(),
            subject: String::new(),
            by: by.clone(),
            at,
            causation: String::new(),
            payload,
        })
        .await?;
        if !flow.is_empty() {
            return self.review_on_flow(id, &b, &flow, &key, &item).await;
        }
        let w = self.work_on(id, crate::flow::MAIN_FLOW).await?;
        // the verdicts judge each other (the origins of an acceptance): what the batch settled is seen by the next
        let mut seen = w.seen.clone();
        let mut out: Vec<Option<ChangeImpact>> = vec![None; b.verdicts.len()];
        let mut events = Vec::with_capacity(b.verdicts.len());
        let mut pending: Vec<usize> = (0..b.verdicts.len()).collect();
        let checked: Result<()> = async {
            while !pending.is_empty() {
                let mut next = Vec::new();
                let mut last = None;
                for &n in &pending {
                    let v = &b.verdicts[n];
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
                    let Some(vcn) = seen.iter().find(|c| c.id == v.impact).cloned() else {
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
                        match self.check_accepted(id, "", &seen, &vcn).await {
                            Ok(()) => {}
                            Err(e) if e.message.ends_with(ORIGINS_PENDING) => {
                                next.push(n);
                                last = Some(named(e));
                                continue;
                            }
                            Err(e) => return Err(named(e)),
                        }
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
                    if let Some(s) = seen.iter_mut().find(|c| c.id == cn.id) {
                        s.review = r.status.clone();
                        s.reviews.push(r.clone());
                    }
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
                    out[n] = Some(cn);
                }
                if next.len() == pending.len() {
                    // no progress: the origins are not settled by this batch
                    return Err(last.expect("a verdict held"));
                }
                pending = next;
            }
            Ok(())
        }
        .await;
        if let Err(e) = checked {
            // the Go service writes the projection of each verdict before it judges the next one
            self.store.skip_impact_positions(events.len()).await?;
            return Err(e);
        }
        let out: Vec<ChangeImpact> = out.into_iter().flatten().collect();
        self.emit_staged(events).await?;
        Ok(out)
    }

    /// The verdicts of a review on a flow (Go's `reviewTx` with no scope): each its own entry, what the flow sees read
    /// again from the log after each one.
    async fn review_on_flow(
        &mut self,
        id: &ChangeId,
        b: &ReviewBatch,
        flow: &str,
        key: &str,
        item: &str,
    ) -> Result<Vec<ChangeImpact>> {
        let many = b.verdicts.len() > 1;
        let mut out = Vec::with_capacity(b.verdicts.len());
        for (n, v) in b.verdicts.iter().enumerate() {
            let named = |this: &Self, e: GraphError| {
                if !many {
                    return e;
                }
                let what = this.views[id]
                    .fold
                    .impact(&v.impact)
                    .map_or(v.impact.clone(), |cn| cn.key.clone());
                e.with_prefix(&format!("verdict {} ({what}): ", n + 1))
            };
            let r = self.verdict_on_flow(id, b, flow, v, key, item).await;
            match r {
                Ok(cn) => out.push(cn),
                Err(e) => return Err(named(self, e)),
            }
        }
        Ok(out)
    }

    async fn verdict_on_flow(
        &mut self,
        id: &ChangeId,
        b: &ReviewBatch,
        flow: &str,
        v: &Verdict,
        key: &str,
        item: &str,
    ) -> Result<ChangeImpact> {
        self.change_open(id).await?;
        let view = &self.views[id];
        if view.flows.get(flow).is_none() {
            return Err(GraphError::not_found(format!("flow {flow}")));
        }
        if view.flows.effective(flow) != crate::flow::FLOW_OPEN {
            return Err(GraphError::conflict(format!("flow {flow} is not open")));
        }
        let Some(mut cn) = view.fold.impact(&v.impact).cloned() else {
            return Err(GraphError::not_found(format!(
                "change impact {} of change {id}",
                v.impact
            )));
        };
        let Some(vcn) = view.seen(flow).into_iter().find(|c| c.id == v.impact) else {
            return Err(GraphError::not_found(format!(
                "change impact {} is not on the flow {flow:?} of change {id}",
                v.impact
            )));
        };
        if vcn.review != REVIEW_PROPOSED {
            return Err(GraphError::conflict(format!(
                "change impact {} is already {}",
                v.impact, vcn.review
            )));
        }
        if v.accept {
            let seen = view.seen(flow);
            self.check_accepted(id, flow, &seen, &vcn).await?;
        }
        let by = if b.by.is_empty() {
            self.env.by.clone()
        } else {
            b.by.clone()
        };
        let r = Review {
            status: v.status().into(),
            by: by.clone(),
            comment: v.comment.trim().into(),
            at: self.env.now(),
            flow: flow.to_string(),
            execution: b.execution.clone(),
            review_id: key.to_string(),
            ..Review::default()
        };
        cn.reviews.push(r.clone());
        self.emit(vec![ImpactEvent {
            change: id.clone(),
            impact: cn.id.clone(),
            op: ImpactOp::Reviewed,
            flow: flow.to_string(),
            execution: b.execution.clone(),
            by,
            review: Some(r),
            causation: item.to_string(),
            ..ImpactEvent::default()
        }])
        .await?;
        Ok(cn)
    }
}

/// The end of the refusal of an acceptance whose origins are not accepted yet: a batch tries it again once the others
/// are made (Go's `errOriginsPending`).
const ORIGINS_PENDING: &str = "settle the origins first";

/// The review gate of a successor (Go's `checkOrigins`, ADR 0077): a node created by the change with origins is
/// accepted and committed only once the change impact of every origin is accepted (an origin changed by the impact of
/// its parent, `via`: that one).
pub(crate) fn check_origins<I: Borrow<ChangeImpact>>(impacts: &[I], cn: &ChangeImpact, d: &Draft) -> Result<()> {
    let impacts: Vec<&ChangeImpact> = impacts.iter().map(Borrow::borrow).collect();
    if cn.intent != INTENT_CREATED || d.origins.is_empty() {
        return Ok(());
    }
    let node_of = |x: &ChangeImpact| x.post.as_ref().or(x.pre.as_ref()).map(|r| r.id.clone());
    let mut pending = Vec::new();
    for o in &d.origins {
        let mut i = impacts.iter().position(|x| node_of(x).as_ref() == Some(&o.id));
        if let Some(k) = i
            && !impacts[k].via.is_empty()
        {
            let via = &impacts[k].via;
            i = impacts.iter().position(|x| &x.id == via);
        }
        match i {
            None => pending.push(format!("{} (not in the change)", o.id)),
            Some(k) if impacts[k].review != crate::impact::REVIEW_ACCEPTED => {
                pending.push(format!("{} ({})", impacts[k].key, impacts[k].review))
            }
            _ => {}
        }
    }
    if pending.is_empty() {
        return Ok(());
    }
    Err(GraphError::new(
        crate::Kind::Conflict,
        format!(
            "{} derives from nodes whose change impacts are not accepted: {}: conflict: {ORIGINS_PENDING}",
            cn.key,
            pending.join(", ")
        ),
    ))
}
