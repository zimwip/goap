//! The reviews of goap-change (`change_review.go`).

use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{self as domain, Blackboard, ChangeID, ChangeImpact, ChangeItem};
use goap_graphapi as graphapi;
use goap_review as review;
use serde_json::{Map, Value};

use crate::builtin::{Args, Error, json, nullable, obj};
use crate::change::Change;

/// The graph as the review use case reads it (`review.Port`; Go's graph implemented it directly).
pub struct ReviewPort(pub Arc<dyn graphapi::Port>);

#[async_trait::async_trait]
impl review::Port for ReviewPort {
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<domain::Change, review::Error> {
        self.0.change(ctx, id).await.map_err(review::Error::port)
    }

    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, review::Error> {
        self.0
            .add_items(ctx, id, items)
            .await
            .map_err(review::Error::port)
    }

    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: domain::ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, review::Error> {
        self.0
            .impact_node_review_batch(ctx, id, b)
            .await
            .map_err(review::Error::port)
    }
}

/// impactID resolves an impact named by its id or by the key of its node.
pub(crate) fn impact_id(bb: &Blackboard, name: &str) -> Result<String, Error> {
    bb.change
        .nodes
        .iter()
        .find(|n| !n.superseded && (n.id.as_str() == name || n.key == name))
        .map(|n| n.id.to_string())
        .ok_or_else(|| Error::msg(format!("{name} is not in the change")))
}

pub(crate) fn impact_ids(bb: &Blackboard, v: Option<&Value>) -> Result<Vec<String>, Error> {
    let mut out = Vec::new();
    if let Some(Value::Array(list)) = v {
        for x in list {
            out.push(impact_id(bb, x.as_str().unwrap_or(""))?);
        }
    }
    Ok(out)
}

impl Change {
    /// reviews lists the reviews of the change (ADR 0080) and the impacts that await a review on the flow.
    pub(crate) async fn reviews(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        // the blackboard is the view of one flow: the reviews of every flow are in the change
        let full = self.p.graph()?.change(ctx, id).await?;
        let rs = review::reviews(&full.items);
        let flow = full.resolve_flow(&a.str("flow"));
        let awaiting: Vec<Value> = review::awaiting(&full, &flow, &rs, "")
            .iter()
            .map(|cn| {
                Value::Object(obj([
                    ("id", json(&cn.id)),
                    ("key", json(&cn.key)),
                    ("type", json(&cn.r#type)),
                    ("intent", json(&cn.intent)),
                ]))
            })
            .collect();
        Ok(obj([
            ("reviews", nullable(&rs)),
            ("awaiting", Value::Array(awaiting)),
        ]))
    }

    /// review runs the operations of the review object (ADR 0080) as the caller.
    pub(crate) async fn review(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        op: &str,
        a: Args<'_>,
    ) -> Result<Map<String, Value>, Error> {
        let port = self
            .p
            .graph
            .clone()
            .ok_or_else(|| Error::msg("the built-in connector has no graph"))?;
        let svc = review::Service::new(Arc::new(ReviewPort(port)));
        let who = review::actor_of(ctx);
        let id = &bb.change.id;
        let r = match op {
            "review_open" => {
                let mut r = svc
                    .open(ctx, id, &a.str("flow"), &a.str("comment"), &who)
                    .await?;
                let add = impact_ids(bb, a.get("impacts"))?;
                if !add.is_empty() {
                    r = svc
                        .update(
                            ctx,
                            id,
                            &r.key,
                            review::Edit {
                                add,
                                ..Default::default()
                            },
                            &who,
                        )
                        .await?;
                }
                r
            }
            "review_update" => {
                let key = a.required("review")?;
                let mut e = review::Edit {
                    comment: a.get("comment").and_then(Value::as_str).map(String::from),
                    add: impact_ids(bb, a.get("add"))?,
                    remove: impact_ids(bb, a.get("remove"))?,
                    ..Default::default()
                };
                if let Some(Value::Array(entries)) = a.get("entries") {
                    let empty = Map::new();
                    for x in entries {
                        let m = x.as_object().unwrap_or(&empty);
                        let imp = impact_id(bb, &Args(m).str("impact"))?;
                        e.entries.push(review::EntryEdit {
                            impact: imp,
                            comment: m.get("comment").and_then(Value::as_str).map(String::from),
                            outcome: m.get("outcome").and_then(Value::as_str).map(String::from),
                        });
                    }
                }
                svc.update(ctx, id, &key, e, &who).await?
            }
            _ => {
                // review_submit, review_discard
                let key = a.required("review")?;
                if op == "review_submit" {
                    svc.submit(ctx, id, &key, &who).await?
                } else {
                    svc.discard(ctx, id, &key, &who).await?
                }
            }
        };
        Ok(obj([("review", json(r))]))
    }
}
