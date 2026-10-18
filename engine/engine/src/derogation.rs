//! The expiry of derogations (ADR 0075 §2, Go's `derogation.go`).

use chrono::{DateTime, SecondsFormat, Utc};
use goap_ctx::Ctx;
use goap_domain::{CHANGE_ACTIVE, ChangeID, ChangeImpactID, ChangeItem, ITEM_PROPOSED, ItemID};
use serde_json::{Map, Value, json};

use crate::error::{Error, Result};
use crate::executors::{ActionContext, ActionResult, ExecError, ExecResult};
use crate::ports::GraphPort;

fn new_id() -> ItemID {
    ItemID::from(uuid::Uuid::new_v4().to_string())
}

/// expire_derogations applies the expiry of the derogations of a change: for each one open and run out at now it
/// writes a closing version, sends the change impacts it covered back to proposed and starts their verification
/// over. It returns the keys of the derogations it closed (and those closed before an error, with it).
pub async fn expire_derogations(
    ctx: &Ctx,
    g: &dyn GraphPort,
    id: &ChangeID,
    now: DateTime<Utc>,
) -> (Vec<String>, Result<()>) {
    let ctx = goap_authz::with_principal(ctx, goap_authz::system("derogation", &[]));
    let mut closed = Vec::new();
    let bb = match g.blackboard_in(&ctx, id, "").await {
        Ok(bb) => bb,
        Err(e) => return (closed, Err(e.into())),
    };
    let expired = goap_risk::expired(&bb.change, now);
    if expired.is_empty() {
        return (closed, Ok(()));
    }
    let facts: Vec<ChangeItem> = bb
        .change
        .items
        .iter()
        .filter(|it| it.r#type == goap_verify::TYPE_VERIFICATION && bb.change.active(&it.id))
        .cloned()
        .collect();
    let subjects = goap_verify::subjects(&facts);
    for d in expired {
        let item = ChangeItem {
            id: new_id(),
            r#type: goap_risk::TYPE_DEROGATION,
            target: "derogation".into(),
            status: ITEM_PROPOSED,
            data: d.closing_data("expired"),
            produced_by: "derogation.expire".into(),
            ..Default::default()
        };
        if let Err(e) = g.add_items(&ctx, id, vec![item]).await {
            return (
                closed,
                Err(Error::from(e).context(format!("close derogation {}", d.key))),
            );
        }
        closed.push(d.key.clone());
        // what it covered: the effects accepted with a reserve on it, and the change impacts it targets
        let mut impacts: Vec<ChangeImpactID> = Vec::new();
        let mut reset = Vec::new();
        for s in &subjects {
            if s.state == goap_verify::ACCEPTED_WITH_RESERVE
                && s.derogation == d.key
                && !s.impact.is_empty()
            {
                impacts.push(ChangeImpactID::from(s.impact.as_str()));
                reset.push(s.clone());
            }
        }
        for cn in &bb.change.nodes {
            if (cn.id.as_str() == d.target || cn.key == d.target) && !impacts.contains(&cn.id) {
                impacts.push(cn.id.clone());
            }
        }
        if impacts.is_empty() {
            continue;
        }
        let comment = format!(
            "derogation {} expired ({}): to be reviewed again",
            d.key,
            d.expires.to_rfc3339_opts(SecondsFormat::Secs, true)
        );
        if let Err(e) = g.reopen_impacts(&ctx, id, impacts, &comment).await {
            return (
                closed,
                Err(Error::from(e).context(format!("reopen the effects of derogation {}", d.key))),
            );
        }
        for s in reset {
            let mut data = Map::new();
            data.insert(goap_verify::KEY_STATE.into(), json!(goap_verify::PRODUCED));
            data.insert(goap_verify::KEY_ACTION.into(), json!(s.action));
            data.insert(goap_verify::KEY_IMPACTS.into(), json!([s.impact]));
            data.insert(goap_verify::KEY_ORACLE.into(), json!(s.oracle));
            data.insert(goap_verify::KEY_INDEPENDENT.into(), json!(s.independent));
            if !s.model.is_empty() {
                data.insert(goap_verify::KEY_MODEL.into(), json!(s.model));
            }
            // the producer stays the original one: the independence of the next verification does not move
            let it = ChangeItem {
                id: new_id(),
                r#type: goap_verify::TYPE_VERIFICATION,
                target: goap_verify::PRODUCED.into(),
                data,
                produced_by: s.producer.clone(),
                execution: s.execution.clone(),
                ..Default::default()
            };
            if let Err(e) = g.add_items(&ctx, id, vec![it]).await {
                return (
                    closed,
                    Err(Error::from(e).context(format!("restart the verification of {}", s.impact))),
                );
            }
        }
    }
    (closed, Ok(()))
}

/// expire_all_derogations is the builtin derogation.expire: it applies the expiry to every live change that holds an
/// open derogation.
pub async fn expire_all_derogations(ctx: &Ctx, ac: &ActionContext) -> ExecResult {
    let changes = ac
        .graph
        .list_changes(
            ctx,
            goap_graphapi::ChangesFilter {
                status: vec![CHANGE_ACTIVE],
                ..Default::default()
            },
        )
        .await
        .map_err(Error::from)?;
    let now = Utc::now();
    let mut closed = Vec::new();
    for c in changes {
        if goap_risk::expired(&c, now).is_empty() {
            continue;
        }
        let (keys, res) = expire_derogations(ctx, &*ac.graph, &c.id, now).await;
        for k in keys {
            closed.push(format!("{}/{k}", c.id));
        }
        if let Err(error) = res {
            return Err(ExecError {
                partial: ActionResult {
                    output: format!("{} derogation(s) closed", closed.len()),
                    ..Default::default()
                },
                error,
            });
        }
    }
    let mut vars = Map::new();
    vars.insert("swept".into(), Value::Bool(true));
    Ok(ActionResult {
        output: format!("{} derogation(s) closed", closed.len()),
        vars_set: vars,
        ..Default::default()
    })
}
