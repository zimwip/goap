//! Port of `settings.go`: the staged settings (ADR 0037 §3, ADR 0122).
//!
//! What the settings dialog edits is graph data staged in the personal changes of the caller, one per namespace: an
//! edit checks the node out and writes its draft, a removal is the transition that retires it, Save accepts and
//! applies, Discard purges. These operations do the steps the dialog used to drive itself, with the same gates as the
//! RPCs they replace (the creation of a node is authorized like ImpactNodeCreate, every node of an adminOnly type
//! needs the access permission), and answer the whole pending state so the dialog needs no read after a write.

use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, LazyLock};

use goap_access as access;
use goap_connect::{ConnectError, Ctx, Request, Response};
use goap_domain::{
    CHANGE_ACTIVE, CHANGE_COMMITTED, CHANGE_DRAFT, Change, ChangeEvent, ChangeID, ChangeImpact,
    ChangeImpactID, INTENT_CREATED, ImpactVerdict, MAIN_BRANCH, REVIEW_ACCEPTED, REVIEW_REJECTED,
    ReviewBatch,
};
use goap_events as events;
use goap_gen::graph::v1 as pb;
use goap_graph::{
    ChangesFilter, Error, ErrorKind, NewChange, NodeCheckout, NodeCreate, NodeTransition,
    NodeUpdate,
};
use goap_pbconv as pbconv;

use crate::handler::Handler;
use crate::personal::{OWNER_ME, subject_of};

/// stateActive is the state a restored node goes back to (lifecycle config, ADR 0076).
const STATE_ACTIVE: &str = "active";

type Rpc<T> = std::result::Result<Response<T>, ConnectError>;
type GResult<T> = std::result::Result<T, ConnectError>;

/// settingsLocks serializes the settings operations of a subject in this process: an edit reads the pending state and
/// builds on it, as the dialog did one edit after the other.
static SETTINGS_LOCKS: LazyLock<parking_lot::Mutex<HashMap<String, Arc<tokio::sync::Mutex<()>>>>> =
    LazyLock::new(Default::default);

async fn lock_settings(subject: &str) -> tokio::sync::OwnedMutexGuard<()> {
    let m = SETTINGS_LOCKS
        .lock()
        .entry(subject.to_string())
        .or_default()
        .clone();
    m.lock_owned().await
}

/// settingsCaller is the subject of a settings call: a person, never anonymous.
fn settings_caller(ctx: &Ctx) -> GResult<String> {
    let me = subject_of(ctx);
    if me.is_empty() {
        return Err(ConnectError::unauthenticated(
            "settings need an identified caller",
        ));
    }
    Ok(me)
}

/// settingsImpacts are the impacts of a personal change the dialog shows: those of the main flow.
fn settings_impacts(c: &Change) -> Vec<ChangeImpact> {
    c.nodes
        .iter()
        .filter(|cn| cn.flow.is_empty() && !cn.superseded && cn.review != REVIEW_REJECTED)
        .cloned()
        .collect()
}

fn impact_of_key(c: &Change, key: &str) -> Option<ChangeImpact> {
    settings_impacts(c).into_iter().find(|cn| cn.key == key)
}

fn to_connect(e: Error) -> ConnectError {
    goap_rpcerr::to_connect(e)
}

impl Handler {
    /// settingsChanges are the open personal changes of the subject, by namespace (the first one of a namespace wins).
    async fn settings_changes(
        &self,
        ctx: &Ctx,
        subject: &str,
    ) -> GResult<BTreeMap<String, Change>> {
        let cs = self
            .graph
            .list_changes(
                ctx,
                &ChangesFilter {
                    owner_org: access::personal_unit(subject),
                    status: vec![CHANGE_DRAFT, CHANGE_ACTIVE],
                    ..Default::default()
                },
            )
            .await
            .map_err(to_connect)?;
        let mut out = BTreeMap::new();
        for c in cs {
            if c.namespace.is_empty()
                || !c.parent_id.is_empty()
                || !access::is_personal_to(&c, subject)
            {
                continue;
            }
            out.entry(c.namespace.clone()).or_insert(c);
        }
        Ok(out)
    }

    /// pendingSettings reads the pending state of the subject.
    async fn pending_settings(
        &self,
        ctx: &Ctx,
        subject: &str,
    ) -> GResult<pb::PendingSettingsResponse> {
        let by_ns = self.settings_changes(ctx, subject).await?;
        let mut out = pb::PendingSettingsResponse::default();
        for (ns, c) in &by_ns {
            let c = self.graph.change(ctx, &c.id).await.map_err(to_connect)?;
            let mut p = pb::PendingSettings {
                namespace: ns.clone(),
                change_id: c.id.to_string(),
                ..Default::default()
            };
            for cn in settings_impacts(&c) {
                let mut s = pb::StagedSetting {
                    key: cn.key.clone(),
                    r#type: cn.r#type.clone(),
                    created: cn.intent == INTENT_CREATED,
                    change_impact_id: cn.id.to_string(),
                    written: cn.drafted(),
                    ..Default::default()
                };
                if let Some(r) = cn.post.as_ref().or(cn.pre.as_ref()) {
                    // a draft is read through the change (it has no version, ADR 0079)
                    match self.graph.change_node_view(ctx, &c.id, "", r).await {
                        Ok(v) => {
                            s.retire = v.state == access::STATE_RETIRED;
                            if s.key.is_empty() {
                                s.key = v.key.clone();
                            }
                            if s.r#type.is_empty() {
                                s.r#type = v.r#type.clone();
                            }
                            if cn.drafted() {
                                s.props = pbconv::to_struct(&v.properties);
                            }
                        }
                        Err(e) if e.is_not_found() => {}
                        Err(e) => return Err(to_connect(e)),
                    }
                }
                if s.key.is_empty() || s.r#type.is_empty() {
                    continue;
                }
                p.nodes.push(s);
            }
            out.changes.push(p);
        }
        Ok(out)
    }

    /// settingsChange is the personal change of the subject for a namespace, opened on main's head when there is none
    /// (the empty baseline for a namespace nothing was written in yet, ADR 0056).
    async fn settings_change(
        &self,
        ctx: &Ctx,
        by_ns: &mut BTreeMap<String, Change>,
        ns: &str,
    ) -> GResult<Change> {
        if let Some(c) = by_ns.get(ns) {
            return self.graph.change(ctx, &c.id).await.map_err(to_connect);
        }
        let head = self
            .graph
            .branch_head(ctx, ns, MAIN_BRANCH)
            .await
            .map_err(to_connect)?;
        let owner = self.resolve_owner(ctx, OWNER_ME).await?;
        let c = self
            .graph
            .create_change(
                ctx,
                NewChange {
                    title: format!("My {ns} settings"),
                    intent: format!("Change the {ns} settings"),
                    namespace: ns.to_string(),
                    baseline_id: head.id,
                    own_branch: true,
                    owner_org: owner,
                    project_id: self.project_of(ctx, ""),
                    ..Default::default()
                },
            )
            .await
            .map_err(to_connect)?;
        self.publish_change(
            ctx,
            &events::change_subject(c.id.as_str(), events::CHANGE_CREATED),
            ChangeEvent {
                r#type: events::CHANGE_CREATED.into(),
                change: c.clone(),
                ..Default::default()
            },
        )
        .await;
        by_ns.insert(ns.to_string(), c.clone());
        Ok(c)
    }

    /// dropIfEmpty purges a personal change left with no impact: it has no reason to stay.
    async fn drop_if_empty(
        &self,
        ctx: &Ctx,
        by_ns: &mut BTreeMap<String, Change>,
        ns: &str,
    ) -> GResult<()> {
        let Some(c) = by_ns.get(ns) else {
            return Ok(());
        };
        let c = self.graph.change(ctx, &c.id).await.map_err(to_connect)?;
        if !settings_impacts(&c).is_empty() {
            return Ok(());
        }
        self.graph
            .purge_change(ctx, &c.id)
            .await
            .map_err(to_connect)?;
        by_ns.remove(ns);
        Ok(())
    }

    /// stageSetting stages one edit.
    async fn stage_setting(
        &self,
        ctx: &Ctx,
        by_ns: &mut BTreeMap<String, Change>,
        e: &pb::SettingEdit,
    ) -> GResult<()> {
        if e.namespace.is_empty() || e.key.is_empty() || e.r#type.is_empty() {
            return Err(to_connect(Error::wrap(
                ErrorKind::Invalid,
                "a setting names its namespace, type and key",
            )));
        }
        let had = by_ns.contains_key(&e.namespace);
        let c = self.settings_change(ctx, by_ns, &e.namespace).await?;
        let by = goap_authz::principal_from(ctx).subject;
        let cn = impact_of_key(&c, &e.key);
        // the node as the change sees it: its draft, else the version the change was forked from
        let view = match self
            .graph
            .change_node_view_by_key(ctx, &c.id, "", &e.namespace, &e.key)
            .await
        {
            Ok(v) => Some(v),
            Err(err) if err.is_not_found() => None,
            Err(err) => return Err(to_connect(err)),
        };
        let exists = view.as_ref().is_some_and(|v| !v.deleted);
        if let Some(v) = &view
            && exists
            && v.r#type != e.r#type
        {
            return Err(to_connect(Error::wrap(
                ErrorKind::Invalid,
                format!("{} is a {}, not a {}", e.key, v.r#type, e.r#type),
            )));
        }
        self.gate_access(ctx, &e.r#type).await?;
        let mut drafted = cn.as_ref().is_some_and(ChangeImpact::drafted);
        let mut checkout = match &cn {
            Some(cn) => NodeCheckout {
                impact: cn.id.clone(),
                produced_by: by.clone(),
                ..Default::default()
            },
            None => NodeCheckout {
                key: e.key.clone(),
                produced_by: by.clone(),
                ..Default::default()
            },
        };
        let retired = view
            .as_ref()
            .is_some_and(|v| v.state == access::STATE_RETIRED);

        if e.retire {
            if let Some(cn) = &cn
                && cn.intent == INTENT_CREATED
            {
                // a node created by the pending change is simply given up
                self.graph
                    .withdraw_impact(ctx, &c.id, &cn.id, "", "")
                    .await
                    .map_err(to_connect)?;
                return self.drop_if_empty(ctx, by_ns, &e.namespace).await;
            }
            if !exists {
                // a node that exists nowhere has nothing to retire
                if !had {
                    return self.drop_if_empty(ctx, by_ns, &e.namespace).await;
                }
                return Ok(());
            }
            if retired {
                return Ok(());
            }
            if drafted && let Some(cn) = &cn {
                // the edits staged on the node go with it: the retirement starts from the version the node has
                self.graph
                    .impact_node_cancel(ctx, &c.id, &cn.id, "", "")
                    .await
                    .map_err(to_connect)?;
            }
            checkout.rationale = format!("Retire {}", e.key);
            self.graph
                .impact_node_transition(
                    ctx,
                    &c.id,
                    NodeTransition {
                        checkout,
                        to: access::STATE_RETIRED.into(),
                    },
                )
                .await
                .map_err(to_connect)?;
            return Ok(());
        }

        let props = pbconv::to_map(e.props.as_ref());
        if !exists {
            self.check_create(ctx, &c, &e.r#type).await?;
            self.graph
                .impact_node_create(
                    ctx,
                    &c.id,
                    NodeCreate {
                        key: e.key.clone(),
                        r#type: e.r#type.clone(),
                        properties: props,
                        rationale: format!("Create {}", e.key),
                        produced_by: by,
                        ..Default::default()
                    },
                )
                .await
                .map_err(to_connect)?;
            return Ok(());
        }
        if retired {
            // a retired node is restored first: a transition of its own, then it is edited
            checkout.rationale = format!("Restore {}", e.key);
            let out = self
                .graph
                .impact_node_transition(
                    ctx,
                    &c.id,
                    NodeTransition {
                        checkout: checkout.clone(),
                        to: STATE_ACTIVE.into(),
                    },
                )
                .await
                .map_err(to_connect)?;
            checkout = NodeCheckout {
                impact: out.id,
                produced_by: by.clone(),
                ..Default::default()
            };
            drafted = true;
        }
        if !drafted {
            checkout.rationale = format!("Change {}", e.key);
            let out = self
                .graph
                .impact_node_checkout(ctx, &c.id, checkout.clone())
                .await
                .map_err(to_connect)?;
            checkout.impact = out.id;
        }
        if props.is_empty() {
            return Ok(());
        }
        self.graph
            .impact_node_update(
                ctx,
                &c.id,
                &checkout.impact,
                NodeUpdate {
                    properties: props,
                    ..Default::default()
                },
            )
            .await
            .map_err(to_connect)?;
        Ok(())
    }

    /// GetPendingSettings answers the edits of the settings dialog the caller has not saved yet (an earlier session may
    /// have left some).
    pub(crate) async fn get_pending_settings_rpc(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetPendingSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        let ctx = &self.identity.context(ctx, &r.headers);
        let me = settings_caller(ctx)?;
        let _lock = lock_settings(&me).await;
        Ok(Response::new(self.pending_settings(ctx, &me).await?))
    }

    /// StageSettings stages edits in order; the first refused one stops the rest (those before it stay staged).
    pub(crate) async fn stage_settings_rpc(
        &self,
        ctx: &Ctx,
        r: Request<pb::StageSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        let ctx = &self.identity.context(ctx, &r.headers);
        let me = settings_caller(ctx)?;
        let _lock = lock_settings(&me).await;
        let mut by_ns = self.settings_changes(ctx, &me).await?;
        for e in &r.msg.edits {
            Box::pin(self.stage_setting(ctx, &mut by_ns, e)).await?;
        }
        Ok(Response::new(self.pending_settings(ctx, &me).await?))
    }

    /// SaveSettings accepts the staged edits that modify something, in one review per change, and applies the
    /// changes; an edit never written or put back as it was is withdrawn (only a modification is accepted, ADR 0102),
    /// and a change left with nothing is removed.
    pub(crate) async fn save_settings_rpc(
        &self,
        ctx: &Ctx,
        r: Request<pb::SaveSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        let ctx = &self.identity.context(ctx, &r.headers);
        let me = settings_caller(ctx)?;
        let _lock = lock_settings(&me).await;
        let by_ns = self.settings_changes(ctx, &me).await?;
        for c in by_ns.values() {
            Box::pin(self.save_settings(ctx, &c.id, &me))
                .await
                .map_err(to_connect)?;
        }
        Ok(Response::new(self.pending_settings(ctx, &me).await?))
    }

    async fn save_settings(&self, ctx: &Ctx, id: &ChangeID, me: &str) -> goap_graph::Result<()> {
        let c = self.graph.change_checked(ctx, id).await?;
        let mut drop: Vec<ChangeImpactID> = Vec::new();
        let mut b = ReviewBatch {
            by: me.to_string(),
            ..Default::default()
        };
        for cn in settings_impacts(&c) {
            if !cn.drafted() || cn.validation.as_ref().is_some_and(|v| !v.modified) {
                drop.push(cn.id.clone());
                continue;
            }
            // Save is an explicit act of the owner on a personal change (ADR 0037): they review their own staged edits
            // here, it is not a side effect of editing (an edit never reviews, ADR 0079). The acceptance runs the
            // validations on the draft; applying the change writes the versions.
            b.verdicts.push(ImpactVerdict {
                impact: cn.id.clone(),
                status: REVIEW_ACCEPTED,
                comment: "Saved by its owner".into(),
            });
        }
        if !drop.is_empty() {
            self.graph.withdraw_impacts(ctx, id, &drop, "", "").await?;
        }
        if b.verdicts.is_empty() {
            // nothing left to save: the change goes, it landed nothing
            self.graph.purge_change(ctx, id).await?;
            return Ok(());
        }
        self.graph.impact_node_review_batch(ctx, id, b).await?;
        let bl = Box::pin(self.graph.apply(ctx, id, "")).await?;
        if let Ok(mut c) = self.graph.change(ctx, id).await
            && c.status != CHANGE_COMMITTED
        {
            c.items.clear();
            self.publish_change(
                ctx,
                &events::change_subject(id.as_str(), events::CHANGE_APPLIED),
                ChangeEvent {
                    r#type: events::CHANGE_APPLIED.into(),
                    change: c,
                    baseline: Some(bl),
                    ..Default::default()
                },
            )
            .await;
        }
        Ok(())
    }

    /// DiscardSettings removes the personal changes holding the staged edits.
    pub(crate) async fn discard_settings_rpc(
        &self,
        ctx: &Ctx,
        r: Request<pb::DiscardSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        let ctx = &self.identity.context(ctx, &r.headers);
        let me = settings_caller(ctx)?;
        let _lock = lock_settings(&me).await;
        let by_ns = self.settings_changes(ctx, &me).await?;
        for c in by_ns.values() {
            self.graph
                .purge_change(ctx, &c.id)
                .await
                .map_err(to_connect)?;
        }
        Ok(Response::new(self.pending_settings(ctx, &me).await?))
    }
}
