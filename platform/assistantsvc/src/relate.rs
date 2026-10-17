//! Attaching and detaching the change of the frame (ADR 0106, `relate.go`): on a change, the assistant may propose to
//! attach it to a change integrating it (a release taking a candidate) or to detach it from the change integrating it
//! (deferred, rejected). Like start_agent, the tool starts nothing: it records a proposal the person confirms
//! (ConfirmAction), which then attaches or detaches the change as them.

use goap_authz::{Principal, with_principal};
use goap_convsvc as convsvc;
use goap_ctx::Ctx;
use goap_domain::ChangeID;
use serde_json::{Map, Value, json};

use crate::agents::{
    ACTION_START_AGENT, Decider, STATUS_FAILED, STATUS_PROPOSED, STATUS_REJECTED, STATUS_STARTING,
};
use crate::service::{Error, MAX_ERROR_TEXT, Service};
use crate::turn::{Turn, action};
use crate::uitools::{ACTION_UI_TOOL, STATUS_DONE};
use crate::util::{Args, clip, go_v, map_of, q, sv};

pub const TOOL_ATTACH_CHANGE: &str = "attach_change";
pub const TOOL_DETACH_CHANGE: &str = "detach_change";
/// The proposals the tools record.
pub const ACTION_ATTACH_CHANGE: &str = "attach_change";
pub const ACTION_DETACH_CHANGE: &str = "detach_change";

/// isProposal reports whether an action type is a proposal the person decides.
pub(crate) fn is_proposal(t: &str) -> bool {
    matches!(
        t,
        ACTION_START_AGENT | ACTION_UI_TOOL | ACTION_ATTACH_CHANGE | ACTION_DETACH_CHANGE
    )
}

impl Turn {
    /// relateChange records a proposal to attach the change of the frame to a parent, or to detach it.
    pub(crate) async fn relate_change(
        &mut self,
        ctx: &Ctx,
        attach: bool,
        a: &Args,
    ) -> Result<Value, String> {
        if self.actions.iter().any(|prev| {
            let t = sv(prev, "type");
            t == ACTION_ATTACH_CHANGE || t == ACTION_DETACH_CHANGE
        }) {
            return Err(
                "one proposal per answer: the person decides the first before another is made"
                    .into(),
            );
        }
        let id = self.in_.context.change();
        if id.is_empty() {
            return Err(
                "no change is open: the person must look at the change to attach or detach".into(),
            );
        }
        let ch = self.s.read_change(ctx, &self.p, &id).await?;
        let mut params = Map::new();
        params.insert("changeId".into(), json!(id));
        params.insert("project".into(), json!(ch.project_id));
        let label;
        if attach {
            let parent_id = a.str("parent");
            if parent_id.is_empty() {
                return Err(r#""parent" is required: the id of the change integrating it (search_changes finds it)"#.into());
            }
            if !ch.parent_id.is_empty() {
                return Err(format!(
                    "change {} is already a sub-change of {}: detach it first",
                    q(&id),
                    q(ch.parent_id.as_str())
                ));
            }
            let parent = self.s.read_change(ctx, &self.p, &parent_id).await?;
            if parent.id == ch.id {
                return Err("a change is not attached to itself".into());
            }
            params.insert("parentId".into(), json!(parent_id));
            label = format!("Attach {} to {}", q(&ch.title), q(&parent.title));
        } else {
            if ch.parent_id.is_empty() {
                return Err(format!(
                    "change {} is not attached to another change",
                    q(&id)
                ));
            }
            params.insert("parentId".into(), json!(ch.parent_id.as_str()));
            label = format!("Detach {} from the change integrating it", q(&ch.title));
        }
        let typ = if attach {
            ACTION_ATTACH_CHANGE
        } else {
            ACTION_DETACH_CHANGE
        };
        let mut act = action(
            json!({"type": typ, "status": STATUS_PROPOSED, "label": clip(&label, 200),
            "args": Value::Object(params)}),
        );
        let r = a.str("rationale");
        if !r.is_empty() {
            act.insert("rationale".into(), json!(clip(&r, 500)));
        }
        self.actions.push(act);
        Ok(
            json!({"status": STATUS_PROPOSED, "note": "proposed, awaiting the person's confirmation in the interface: nothing changed yet. Say what you propose and why."}),
        )
    }
}

impl Service {
    /// confirmRelate carries out an accepted attach / detach proposal as the person: re-validated (stale when the
    /// change moved since), claimed, then done (with the change as it is now) or failed.
    pub(crate) async fn confirm_relate(
        &self,
        ctx: &Ctx,
        p: &Principal,
        act: &convsvc::Action,
        d: &Decider<'_>,
    ) -> Result<convsvc::Message, Error> {
        let ar = Args(map_of(act, "args"));
        let ch = self
            .read_change(ctx, p, &ar.str("changeId"))
            .await
            .map_err(Error::Stale)?;
        let attach = sv(act, "type") == ACTION_ATTACH_CHANGE;
        if attach && !ch.parent_id.is_empty() {
            return Err(Error::Stale(format!(
                "change {} is already a sub-change of {}",
                q(ch.id.as_str()),
                q(ch.parent_id.as_str())
            )));
        }
        if !attach && ch.parent_id.as_str() != ar.str("parentId") {
            return Err(Error::Stale(format!(
                "change {} is no longer a sub-change of {}",
                q(ch.id.as_str()),
                q(&ar.str("parentId"))
            )));
        }
        d.decide(STATUS_PROPOSED, STATUS_STARTING, None).await?;
        let pctx = with_principal(ctx, p.clone());
        let out = if attach {
            self.graph
                .attach_change(&pctx, &ch.id, &ChangeID::from(ar.str("parentId").as_str()))
                .await
        } else {
            self.graph.detach_change(&pctx, &ch.id).await
        };
        match out {
            Err(e) => {
                let msg = clip(&e.to_string(), MAX_ERROR_TEXT);
                d.decide(
                    STATUS_STARTING,
                    STATUS_FAILED,
                    Some(Box::new(move |a: &mut convsvc::Action| {
                        a.insert("error".into(), json!(msg));
                    })),
                )
                .await
            }
            Ok(out) => {
                let res = json!({"changeId": out.id.as_str(), "parentId": out.parent_id.as_str()});
                d.decide(
                    STATUS_STARTING,
                    STATUS_DONE,
                    Some(Box::new(move |a: &mut convsvc::Action| {
                        a.insert("result".into(), res);
                    })),
                )
                .await
            }
        }
    }
}

/// describeRelate is what the history tells the model of an attach / detach proposal.
pub(crate) fn describe_relate(a: &convsvc::Action) -> String {
    let ar = Args(map_of(a, "args"));
    let what = if sv(a, "type") == ACTION_DETACH_CHANGE {
        format!(
            "detach change {} from {}",
            ar.str("changeId"),
            ar.str("parentId")
        )
    } else {
        format!(
            "attach change {} to {}",
            ar.str("changeId"),
            ar.str("parentId")
        )
    };
    match sv(a, "status") {
        STATUS_DONE => format!("the person accepted the proposal to {what}: done"),
        STATUS_REJECTED => format!("the person rejected the proposal to {what}: nothing changed"),
        STATUS_FAILED => format!(
            "the person accepted the proposal to {what} but it failed: {}",
            go_v(a.get("error"))
        ),
        _ => format!("proposal to {what}: the person has not decided yet, nothing changed"),
    }
}
