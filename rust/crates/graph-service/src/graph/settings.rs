//! The staged settings (Go's `graphsvc/settings.go`, ADR 0037 §3, ADR 0122): what the settings dialog edits is graph
//! data staged in the personal changes of the caller, one per namespace. An edit checks the node out and writes its
//! draft, a removal is the transition that retires it, Save accepts and applies, Discard purges; each answers the whole
//! pending state.
//!
//! The Go service runs each step as an operation of the graph in a transaction of its own. They run here in one
//! transaction, each step a transaction of the Go service within it ([`Tx::checkpoint`]): a step refused is undone
//! alone and the steps before it stay, as there; anything this service does not serve undoes the whole call, which
//! the Go service then answers from the start.
use std::collections::BTreeMap;

use super::*;
use graph_catalog::catalog::STRUCTURE_ORGANISATION;
use graph_core::write::tx::Rehearsal;

/// The state a restored node goes back to (the lifecycle `config`, ADR 0076).
const STATE_ACTIVE: &str = "active";

/// A step refused: what the call answers once the steps before it are kept.
type Refusal = GraphError;

impl Graph {
    /// Serializes the settings calls of a subject in this process (Go's `lockSettings`): an edit builds on the
    /// pending state.
    async fn lock_settings(&self, subject: &str) -> tokio::sync::OwnedMutexGuard<()> {
        let lock = self
            .state
            .settings
            .lock()
            .await
            .entry(subject.to_string())
            .or_default()
            .clone();
        lock.lock_owned().await
    }

    /// Runs a settings call for an identified caller: its steps, then the pending state is answered; a step refused
    /// keeps the ones before it and answers its refusal.
    async fn settings(&self, caller: Caller, op: Op) -> Answer<pb::PendingSettingsResponse> {
        let me = caller.subject.clone();
        if me.is_empty() {
            return Err(ConnectError::unauthenticated("settings need an identified caller"));
        }
        let _one = self.lock_settings(&me).await;
        let res = self
            .write_on(caller, &[], async |tx, acc| {
                let refused = run(tx, acc, &me, op).await?;
                tx.checkpoint().await?;
                match refused {
                    Some(e) => Ok(Err(e)),
                    None => Ok(Ok(pending(tx, &me).await?)),
                }
            })
            .await?;
        match res.msg {
            Ok(msg) => Ok(Response {
                headers: res.headers,
                trailers: res.trailers,
                msg,
            }),
            Err(err) => {
                let mut e = connect_error(err);
                e.meta_mut().extend(res.headers);
                Err(e)
            }
        }
    }

    pub(super) async fn get_pending_settings_rpc(&self, caller: Caller) -> Answer<pb::PendingSettingsResponse> {
        self.settings(caller, Op::Get).await
    }

    pub(super) async fn stage_settings_rpc(
        &self,
        caller: Caller,
        edits: Vec<pb::SettingEdit>,
    ) -> Answer<pb::PendingSettingsResponse> {
        self.settings(caller, Op::Stage(edits)).await
    }

    pub(super) async fn save_settings_rpc(&self, caller: Caller) -> Answer<pb::PendingSettingsResponse> {
        self.settings(caller, Op::Save).await
    }

    pub(super) async fn discard_settings_rpc(&self, caller: Caller) -> Answer<pb::PendingSettingsResponse> {
        self.settings(caller, Op::Discard).await
    }
}

/// A settings call.
enum Op {
    Get,
    Stage(Vec<pb::SettingEdit>),
    Save,
    Discard,
}

/// The steps of a settings call, until one is refused.
async fn run(tx: &mut Tx<'_, Session>, acc: &Access, me: &str, op: Op) -> Result<Option<Refusal>, GraphError> {
    match op {
        Op::Get => Ok(None),
        Op::Stage(edits) => {
            let mut by_ns = settings_changes(tx, me).await?;
            let mut spaces: Vec<&str> = edits.iter().map(|e| e.namespace.as_str()).collect();
            spaces.extend(by_ns.keys().map(String::as_str));
            served_spaces(tx, &spaces)?;
            for e in &edits {
                if let Err(refused) = (Stage { tx, acc, me }).edit(&mut by_ns, e).await? {
                    return Ok(Some(refused));
                }
            }
            Ok(None)
        }
        Op::Save => {
            let by_ns = settings_changes(tx, me).await?;
            served_spaces(tx, &by_ns.keys().map(String::as_str).collect::<Vec<_>>())?;
            for c in by_ns.values() {
                if let Err(refused) = (Stage { tx, acc, me }).save(&c.id).await? {
                    return Ok(Some(refused));
                }
            }
            Ok(None)
        }
        Op::Discard => {
            let by_ns = settings_changes(tx, me).await?;
            for c in by_ns.values() {
                if let Err(refused) = step(tx, async |tx| tx.purge_change(&c.id).await.map(|_| ())).await? {
                    return Ok(Some(refused));
                }
            }
            Ok(None)
        }
    }
}

/// The settings of the platform's namespaces are served for a caller administering the platform (`platform_served`):
/// any other caller's are the Go service's.
fn served_spaces(tx: &Tx<'_, Session>, spaces: &[&str]) -> Result<(), GraphError> {
    for ns in spaces {
        platform_served(tx, ns)?;
    }
    Ok(())
}

/// Runs one operation of the graph as the Go service runs it, in a transaction of its own: refused (the inner
/// error), it is undone and the call stops there; not served, the whole call is undone.
async fn step<T, F>(tx: &mut Tx<'_, Session>, f: F) -> Result<Result<T, Refusal>, GraphError>
where
    F: for<'a, 'b> AsyncFnOnce(&'a mut Tx<'b, Session>) -> Result<T, GraphError>,
{
    let r: Rehearsal = tx.rehearse().await?;
    match f(tx).await {
        Ok(v) => {
            tx.checkpoint().await?;
            Ok(Ok(v))
        }
        Err(e) if e.kind == Kind::Unimplemented => Err(e),
        Err(e) => {
            tx.rewind(r).await?;
            Ok(Err(e))
        }
    }
}

/// Runs a step, returning from the caller with its refusal.
macro_rules! step {
    ($tx:expr, $f:expr) => {
        match step($tx, $f).await? {
            Ok(v) => v,
            Err(refused) => return Ok(Err(refused)),
        }
    };
}

/// The open personal changes of the subject, by namespace, the first one of a namespace winning (Go's
/// `settingsChanges`).
async fn settings_changes(tx: &Tx<'_, Session>, subject: &str) -> Result<BTreeMap<String, change::Change>, GraphError> {
    let unit = format!("{}{subject}", change::USER_PREFIX);
    let f = ChangesFilter {
        owner_org: tx.structure_url(STRUCTURE_ORGANISATION, &unit)?.to_string(),
        status: vec!["draft".into(), "active".into()],
        ..ChangesFilter::default()
    };
    let mut out = BTreeMap::new();
    for c in tx.store.change_heads(&f).await? {
        if c.namespace.is_empty() || !c.parent_id.0.is_empty() || !c.is_personal_to(subject) {
            continue;
        }
        out.entry(c.namespace.clone()).or_insert(c);
    }
    Ok(out)
}

/// The impacts of a personal change the dialog shows: those of the main flow (Go's `settingsImpacts`).
fn settings_impacts(w: &whole::WholeChange) -> Vec<(ChangeImpact, Option<whole::Validation>)> {
    w.impacts
        .iter()
        .filter(|(cn, _)| cn.flow.is_empty() && !cn.superseded && cn.review != graph_core::impact::REVIEW_REJECTED)
        .cloned()
        .collect()
}

/// A change read whole in the transaction.
async fn whole_of(tx: &Tx<'_, Session>, id: &ChangeId) -> Result<whole::WholeChange, GraphError> {
    whole::whole_change(tx.store, tx.catalog(), id).await
}

/// A node as a personal change sees it, by reference (Go's `ChangeNodeView`): a draft through the change, a version
/// as it is.
async fn node_in(tx: &Tx<'_, Session>, id: &ChangeId, r: &NodeRef) -> Result<graph_core::model::Node, GraphError> {
    if r.version != 0 {
        return read::stored_node(tx.store, r).await;
    }
    let view = graph_core::view::View {
        change: id.clone(),
        ..Default::default()
    };
    let w = World::open(tx.store, &view).await?;
    Ok(w.node_view(&r.id, "").await?.node)
}

/// A node as a personal change sees it, by key (Go's `ChangeNodeViewByKey`): its draft, else the version the change
/// was forked from.
async fn node_by_key(tx: &Tx<'_, Session>, id: &ChangeId, key: &str) -> Result<graph_core::model::Node, GraphError> {
    let view = graph_core::view::View {
        change: id.clone(),
        ..Default::default()
    };
    let w = World::open(tx.store, &view).await?;
    Ok(w.node_view(&NodeId::default(), key).await?.node)
}

/// The pending state of the subject (Go's `pendingSettings`).
async fn pending(tx: &Tx<'_, Session>, subject: &str) -> Result<pb::PendingSettingsResponse, GraphError> {
    let mut out = pb::PendingSettingsResponse::default();
    for (ns, c) in settings_changes(tx, subject).await? {
        let w = whole_of(tx, &c.id).await?;
        let mut p = pb::PendingSettings {
            namespace: ns,
            change_id: c.id.0.clone(),
            nodes: Vec::new(),
        };
        for (cn, _) in settings_impacts(&w) {
            let mut s = pb::StagedSetting {
                key: cn.key.clone(),
                r#type: cn.typ.clone(),
                created: cn.intent == graph_core::impact::INTENT_CREATED,
                change_impact_id: cn.id.clone(),
                written: cn.drafted(),
                ..Default::default()
            };
            if let Some(r) = cn.post.as_ref().or(cn.pre.as_ref()) {
                match node_in(tx, &c.id, r).await {
                    Ok(v) => {
                        s.retire = v.state == access::STATE_RETIRED;
                        if s.key.is_empty() {
                            s.key = v.key.clone();
                        }
                        if s.r#type.is_empty() {
                            s.r#type = v.typ.clone();
                        }
                        if cn.drafted() {
                            s.props = convert::props(&v.props);
                        }
                    }
                    Err(e) if e.is_not_found() => {}
                    Err(e) => return Err(e),
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

/// The steps of a settings call for the caller.
struct Stage<'t, 'a, 'b, 'm> {
    tx: &'t mut Tx<'a, Session>,
    acc: &'b Access,
    me: &'m str,
}

impl Stage<'_, '_, '_, '_> {
    /// The personal change of the subject for a namespace, opened on main's head when there is none (Go's
    /// `settingsChange`).
    async fn change(
        &mut self,
        by_ns: &mut BTreeMap<String, change::Change>,
        ns: &str,
    ) -> Result<Result<change::Change, Refusal>, GraphError> {
        if let Some(c) = by_ns.get(ns) {
            return Ok(Ok(c.clone()));
        }
        let head = match read::branch_head(self.tx.store, ns, MAIN_BRANCH).await {
            Ok(h) => h,
            Err(e) if e.kind == Kind::Unimplemented => return Err(e),
            Err(e) => return Ok(Err(e)),
        };
        let project = if self.acc.project.is_empty() {
            self.tx.structure_root(STRUCTURE_PROJECT)?
        } else {
            self.acc.project.clone()
        };
        let owner = format!("{}{}", change::USER_PREFIX, self.me);
        let c = step!(self.tx, async |tx| {
            if project_stopped(tx, &project).await? {
                return Err(GraphError::new(
                    Kind::Conflict,
                    format!("project {project} is stopped: it takes no new change"),
                ));
            }
            tx.create_change(NewChange {
                title: format!("My {ns} settings"),
                intent: format!("Change the {ns} settings"),
                namespace: ns.to_string(),
                baseline_id: head.id.clone(),
                own_branch: true,
                owner_org: owner.clone(),
                project_id: project.clone(),
                ..NewChange::default()
            })
            .await
        });
        by_ns.insert(ns.to_string(), c.clone());
        Ok(Ok(c))
    }

    /// Purges a personal change left with no impact (Go's `dropIfEmpty`).
    async fn drop_if_empty(
        &mut self,
        by_ns: &mut BTreeMap<String, change::Change>,
        ns: &str,
    ) -> Result<Result<(), Refusal>, GraphError> {
        let Some(c) = by_ns.get(ns).cloned() else {
            return Ok(Ok(()));
        };
        if !settings_impacts(&whole_of(self.tx, &c.id).await?).is_empty() {
            return Ok(Ok(()));
        }
        step!(self.tx, async |tx| tx.purge_change(&c.id).await.map(|_| ()));
        by_ns.remove(ns);
        Ok(Ok(()))
    }

    /// Stages one edit (Go's `stageSetting`).
    async fn edit(
        &mut self,
        by_ns: &mut BTreeMap<String, change::Change>,
        e: &pb::SettingEdit,
    ) -> Result<Result<(), Refusal>, GraphError> {
        if e.namespace.is_empty() || e.key.is_empty() || e.r#type.is_empty() {
            return Ok(Err(GraphError::invalid("a setting names its namespace, type and key")));
        }
        let had = by_ns.contains_key(&e.namespace);
        let c = match self.change(by_ns, &e.namespace).await? {
            Ok(c) => c,
            Err(refused) => return Ok(Err(refused)),
        };
        let w = whole_of(self.tx, &c.id).await?;
        let cn = settings_impacts(&w)
            .into_iter()
            .map(|(cn, _)| cn)
            .find(|cn| cn.key == e.key);
        let view = match node_by_key(self.tx, &c.id, &e.key).await {
            Ok(v) => Some(v),
            Err(err) if err.is_not_found() => None,
            Err(err) if err.kind == Kind::Unimplemented => return Err(err),
            Err(err) => return Ok(Err(err)),
        };
        let exists = view.as_ref().is_some_and(|v| !v.deleted);
        if let Some(v) = view.as_ref().filter(|_| exists)
            && v.typ != e.r#type
        {
            return Ok(Err(GraphError::invalid(format!(
                "{} is a {}, not a {}",
                e.key, v.typ, e.r#type
            ))));
        }
        if let Err(refused) = self.acc.gate_access(self.tx.catalog(), &e.r#type) {
            return Ok(Err(refused));
        }
        let mut drafted = cn.as_ref().is_some_and(ChangeImpact::drafted);
        let by = self.acc.who.subject.clone();
        let mut checkout = match &cn {
            Some(cn) => NodeCheckout {
                impact: cn.id.clone(),
                produced_by: by.clone(),
                ..NodeCheckout::default()
            },
            None => NodeCheckout {
                key: e.key.clone(),
                produced_by: by.clone(),
                ..NodeCheckout::default()
            },
        };
        let retired = view.as_ref().is_some_and(|v| v.state == access::STATE_RETIRED);
        let acc = self.acc;
        let authorize = |node: &str, key: &str, t: &Transition| acc.transition(node, key, t);

        if e.retire {
            if let Some(cn) = cn.as_ref().filter(|cn| cn.intent == graph_core::impact::INTENT_CREATED) {
                // a node created by the pending change is simply given up
                step!(self.tx, async |tx| tx
                    .withdraw_impacts(&c.id, std::slice::from_ref(&cn.id), "", "")
                    .await);
                return self.drop_if_empty(by_ns, &e.namespace).await;
            }
            if !exists {
                // a node that exists nowhere has nothing to retire
                if !had {
                    return self.drop_if_empty(by_ns, &e.namespace).await;
                }
                return Ok(Ok(()));
            }
            if retired {
                return Ok(Ok(()));
            }
            if drafted && let Some(cn) = &cn {
                // the edits staged on the node go with it: the retirement starts from the version the node has
                step!(self.tx, async |tx| tx.impact_node_cancel(&c.id, &cn.id, "", "").await);
            }
            checkout.rationale = format!("Retire {}", e.key);
            step!(self.tx, async |tx| tx
                .impact_node_transition(&c.id, "", checkout, access::STATE_RETIRED, &authorize)
                .await);
            return Ok(Ok(()));
        }

        let props = e.props.as_ref().map(convert::map).unwrap_or_default();
        if !exists {
            if let Err(refused) = self.acc.check_create(&c, self.tx.catalog(), &e.r#type) {
                return Ok(Err(refused));
            }
            step!(self.tx, async |tx| tx
                .impact_node_create(
                    &c.id,
                    NodeCreate {
                        key: e.key.clone(),
                        typ: e.r#type.clone(),
                        props: Some(props.clone()).filter(|p| !p.is_empty()),
                        rationale: format!("Create {}", e.key),
                        produced_by: by.clone(),
                        ..NodeCreate::default()
                    },
                    "",
                )
                .await);
            return Ok(Ok(()));
        }
        if retired {
            // a retired node is restored first: a transition of its own, then it is edited
            checkout.rationale = format!("Restore {}", e.key);
            let out = step!(self.tx, async |tx| tx
                .impact_node_transition(&c.id, "", checkout.clone(), STATE_ACTIVE, &authorize)
                .await);
            checkout = NodeCheckout {
                impact: out.id,
                produced_by: by.clone(),
                ..NodeCheckout::default()
            };
            drafted = true;
        }
        if !drafted {
            checkout.rationale = format!("Change {}", e.key);
            let out = step!(self.tx, async |tx| tx
                .impact_node_checkout(&c.id, "", vec![checkout.clone()])
                .await);
            checkout.impact = out.into_iter().next().map(|cn| cn.id).unwrap_or_default();
        }
        if props.is_empty() {
            return Ok(Ok(()));
        }
        step!(self.tx, async |tx| tx
            .impact_node_update(&c.id, &checkout.impact, props.clone(), "", "", "")
            .await);
        Ok(Ok(()))
    }

    /// Accepts the staged edits that modify something, in one review, and applies the change; an edit never written
    /// or put back as it was is withdrawn, a change left with nothing is removed (Go's `saveSettings`).
    async fn save(&mut self, id: &ChangeId) -> Result<Result<(), Refusal>, GraphError> {
        let w = whole_of(self.tx, id).await?;
        let mut drop = Vec::new();
        let mut verdicts = Vec::new();
        for (cn, v) in settings_impacts(&w) {
            if !cn.drafted() || v.as_ref().is_some_and(|v| !v.modified) {
                drop.push(cn.id.clone());
                continue;
            }
            // Save is an explicit act of the owner on a personal change (ADR 0037): they review their own staged
            // edits here; the acceptance runs the validations on the draft, applying the change writes the versions
            verdicts.push(Verdict {
                impact: cn.id.clone(),
                accept: true,
                comment: "Saved by its owner".into(),
            });
        }
        if !drop.is_empty() {
            step!(self.tx, async |tx| tx.withdraw_impacts(id, &drop, "", "").await);
        }
        if verdicts.is_empty() {
            // nothing left to save: the change goes, it committed nothing
            step!(self.tx, async |tx| tx.purge_change(id).await.map(|_| ()));
            return Ok(Ok(()));
        }
        let b = ReviewBatch {
            by: self.me.to_string(),
            verdicts,
            ..ReviewBatch::default()
        };
        step!(self.tx, async |tx| tx.impact_node_review_batch(id, b).await);
        step!(self.tx, async |tx| tx.commit_change(id, "").await);
        // a merge that rebased and found conflicts keeps the rebase (Go's `IntegrateChange`)
        let merged = step!(self.tx, async |tx| tx.merge_change(id, &Default::default()).await);
        if let Merge::Rebased(err) = merged {
            return Ok(Err(err));
        }
        Ok(Ok(()))
    }
}
