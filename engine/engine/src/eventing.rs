//! Port of `eventing.go`: an in-process graph reporting the change lifecycle events.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Blackboard, BoardIssue, Branch, CHANGE_APPLIED, CHANGE_COMMITTED, Change,
    ChangeEvent, ChangeID, ChangeImpact, ChangeImpactID, ChangeItem, DecisionPoint, Flow, Link,
    LinkID, LogEntry, LogFilter, Node, NodeID, NodeReview, ReviewBatch, Structures, TYPE_SIGNAL,
};
use goap_graphapi::{
    ChangeDependencies, ChangePatch, ChangesFilter, Error, LinkWrite, MergeInput, MergePlan,
    NewChange, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate, OpenDecisionRequest,
    OpenFlowRequest, OpenOptionRequest, OptionComparison, RebaseState, Resolution, Restructured,
    RuleRequest, SplitInput,
};

use crate::broker::Broker;
use crate::ports::GraphPort;
use crate::process::{STATUS_COMPLETED, STATUS_FAILED, STATUS_STUCK};
use crate::triggers::{TriggerEvent, TriggerManager};

/// OnEvent receives the change lifecycle events of an EventingGraph.
pub type OnEvent = Arc<dyn Fn(&Ctx, ChangeEvent) + Send + Sync>;

/// EventingGraph wraps an in-process GraphPort and reports change lifecycle events (single-process deployments,
/// where the graph service does not publish them on NATS).
pub struct EventingGraph {
    pub inner: Arc<dyn GraphPort>,
    pub on_event: Option<OnEvent>,
}

impl EventingGraph {
    pub fn new(inner: Arc<dyn GraphPort>, on_event: Option<OnEvent>) -> EventingGraph {
        EventingGraph { inner, on_event }
    }

    async fn emit(
        &self,
        ctx: &Ctx,
        typ: &str,
        id: &ChangeID,
        b: Option<Baseline>,
        items: Vec<ChangeItem>,
    ) {
        let Some(on) = &self.on_event else {
            return;
        };
        let Ok(bb) = self.inner.blackboard(ctx, id).await else {
            return;
        };
        let mut c = bb.change;
        c.items = Vec::new();
        on(
            ctx,
            ChangeEvent {
                r#type: typ.to_string(),
                change: c,
                baseline: b,
                items,
                end_user: goap_authz::principal_from(ctx).end_user().to_string(),
                ..Default::default()
            },
        );
    }
}

/// trigger_event_of converts a change event.
pub fn trigger_event_of(ev: ChangeEvent) -> TriggerEvent {
    TriggerEvent {
        r#type: ev.r#type,
        change: Some(ev.change),
        items: ev.items,
        fields: ev.fields,
        end_user: ev.end_user,
        ..Default::default()
    }
}

impl TriggerManager {
    /// watch_processes feeds process events of the broker to the trigger manager (process.completed,
    /// process.failed, process.stuck). It returns when ctx is done.
    pub async fn watch_processes(self: &Arc<Self>, ctx: &Ctx, b: &Broker) {
        let (mut evs, _unsubscribe) = b.subscribe(256);
        loop {
            let ev = tokio::select! {
                _ = ctx.done() => return,
                ev = evs.recv() => match ev { Some(ev) => ev, None => return },
            };
            let Some(p) = ev.process.clone() else {
                continue;
            };
            if let Some(typ) = goap_events::from_broker(&ev.event) {
                self.handle(
                    ctx,
                    TriggerEvent {
                        r#type: typ.to_string(),
                        process: Some(p),
                        step: ev.step.clone(),
                        ..Default::default()
                    },
                )
                .await;
                continue;
            }
            let typ = match p.status.as_str() {
                STATUS_COMPLETED if ev.event == STATUS_COMPLETED => goap_events::PROCESS_COMPLETED,
                STATUS_FAILED if ev.event == STATUS_FAILED => goap_events::PROCESS_FAILED,
                STATUS_STUCK if ev.event == STATUS_STUCK => goap_events::PROCESS_STUCK,
                _ => continue,
            };
            self.handle(
                ctx,
                TriggerEvent {
                    r#type: typ.to_string(),
                    process: Some(p),
                    ..Default::default()
                },
            )
            .await;
        }
    }
}

#[async_trait::async_trait]
impl goap_graphapi::Port for EventingGraph {
    async fn create_change(&self, ctx: &Ctx, in_: NewChange) -> Result<Change, Error> {
        let c = self.inner.create_change(ctx, in_).await?;
        self.emit(ctx, goap_events::CHANGE_CREATED, &c.id, None, Vec::new())
            .await;
        Ok(c)
    }

    async fn attach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change, Error> {
        let c = self.inner.attach_change(ctx, id, parent).await?;
        self.emit(ctx, goap_events::CHANGE_ATTACHED, id, None, Vec::new())
            .await;
        Ok(c)
    }

    async fn detach_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        let before = self.inner.change(ctx, id).await?;
        let c = self.inner.detach_change(ctx, id).await?;
        if let Some(on) = &self.on_event {
            let mut ev = c.clone();
            ev.items = Vec::new();
            ev.nodes = Vec::new();
            ev.parent_id = before.parent_id;
            on(
                ctx,
                ChangeEvent {
                    r#type: goap_events::CHANGE_DETACHED.into(),
                    change: ev,
                    end_user: goap_authz::principal_from(ctx).end_user().to_string(),
                    ..Default::default()
                },
            );
        }
        Ok(c)
    }

    async fn update_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        p: ChangePatch,
    ) -> Result<Change, Error> {
        let before = self.inner.change(ctx, id).await?;
        let c = self.inner.update_change(ctx, id, p).await?;
        let fields = goap_domain::edited_fields(&before, &c);
        if !fields.is_empty()
            && let Some(on) = &self.on_event
        {
            let mut ev = c.clone();
            ev.items = Vec::new();
            ev.nodes = Vec::new();
            on(
                ctx,
                ChangeEvent {
                    r#type: goap_events::CHANGE_EDITED.into(),
                    change: ev,
                    fields,
                    end_user: goap_authz::principal_from(ctx).end_user().to_string(),
                    ..Default::default()
                },
            );
        }
        Ok(c)
    }

    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, Error> {
        let out = self.inner.add_items(ctx, id, items).await?;
        self.emit(ctx, goap_events::CHANGE_ITEM_ADDED, id, None, out.clone())
            .await;
        if out.iter().any(|it| it.r#type == TYPE_SIGNAL) {
            self.emit(ctx, goap_events::CHANGE_SIGNAL, id, None, out.clone())
                .await;
        }
        Ok(out)
    }

    async fn apply(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        baseline_name: &str,
    ) -> Result<Baseline, Error> {
        let b = self.inner.apply(ctx, id, baseline_name).await?;
        // a merge waiting for a resolution leaves the change committed, not applied
        match self.inner.change(ctx, id).await {
            Ok(c) if c.status == CHANGE_COMMITTED => {}
            _ => {
                self.emit(
                    ctx,
                    goap_events::CHANGE_APPLIED,
                    id,
                    Some(b.clone()),
                    Vec::new(),
                )
                .await
            }
        }
        Ok(b)
    }

    async fn integrate_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: BTreeMap<NodeID, Resolution>,
    ) -> Result<Change, Error> {
        let c = self.inner.integrate_change(ctx, id, resolutions).await?;
        if c.status == CHANGE_APPLIED {
            self.emit(ctx, goap_events::CHANGE_APPLIED, id, None, Vec::new())
                .await;
        }
        Ok(c)
    }

    async fn move_change(&self, ctx: &Ctx, id: &ChangeID, project: &str) -> Result<Change, Error> {
        self.inner.move_change(ctx, id, project).await
    }

    async fn set_change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        deps: Vec<ChangeID>,
    ) -> Result<Change, Error> {
        self.inner.set_change_dependencies(ctx, id, deps).await
    }

    async fn change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<ChangeDependencies, Error> {
        self.inner.change_dependencies(ctx, id).await
    }

    async fn upgrade_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        version: &str,
    ) -> Result<Change, Error> {
        self.inner.upgrade_change(ctx, id, version).await
    }

    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        self.inner.change(ctx, id).await
    }

    async fn propose_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        nodes: Vec<ChangeImpact>,
    ) -> Result<Vec<ChangeImpact>, Error> {
        self.inner.propose_impact(ctx, id, nodes).await
    }

    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCreate,
    ) -> Result<ChangeImpact, Error> {
        self.inner.impact_node_create(ctx, id, in_).await
    }

    async fn impact_node_checkout(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCheckout,
    ) -> Result<ChangeImpact, Error> {
        self.inner.impact_node_checkout(ctx, id, in_).await
    }

    async fn impact_node_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        in_: NodeUpdate,
    ) -> Result<ChangeImpact, Error> {
        self.inner.impact_node_update(ctx, id, impact, in_).await
    }

    async fn impact_link_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        l: LinkWrite,
        flow: &str,
        execution: &str,
    ) -> Result<Link, Error> {
        self.inner
            .impact_link_create(ctx, id, impact, l, flow, execution)
            .await
    }

    async fn impact_link_delete(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        self.inner
            .impact_link_delete(ctx, id, link, flow, execution)
            .await
    }

    async fn impact_node_transition(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeTransition,
    ) -> Result<ChangeImpact, Error> {
        self.inner.impact_node_transition(ctx, id, in_).await
    }

    async fn impact_node_cancel(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact, Error> {
        self.inner
            .impact_node_cancel(ctx, id, impact, flow, execution)
            .await
    }

    async fn withdraw_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        self.inner
            .withdraw_impact(ctx, id, impact, flow, execution)
            .await
    }

    async fn withdraw_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        self.inner
            .withdraw_impacts(ctx, id, impacts, flow, execution)
            .await
    }

    async fn impact_node_merge(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: MergeInput,
    ) -> Result<Restructured, Error> {
        self.inner.impact_node_merge(ctx, id, in_).await
    }

    async fn impact_node_split(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: SplitInput,
    ) -> Result<Restructured, Error> {
        self.inner.impact_node_split(ctx, id, in_).await
    }

    async fn impact_node_review_on(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        execution: &str,
        node: &ChangeImpactID,
        status: NodeReview,
        by: &str,
        comment: &str,
    ) -> Result<ChangeImpact, Error> {
        self.inner
            .impact_node_review_on(ctx, id, flow, execution, node, status, by, comment)
            .await
    }

    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, Error> {
        self.inner.impact_node_review_batch(ctx, id, b).await
    }

    async fn reopen_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        comment: &str,
    ) -> Result<Vec<ChangeImpactID>, Error> {
        self.inner.reopen_impacts(ctx, id, impacts, comment).await
    }

    async fn blackboard(&self, ctx: &Ctx, id: &ChangeID) -> Result<Blackboard, Error> {
        self.inner.blackboard(ctx, id).await
    }

    async fn blackboard_in(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Blackboard, Error> {
        self.inner.blackboard_in(ctx, id, flow).await
    }

    async fn open_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenFlowRequest,
    ) -> Result<Flow, Error> {
        self.inner.open_flow(ctx, id, in_).await
    }

    async fn adopt_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        by: &str,
    ) -> Result<Flow, Error> {
        self.inner.adopt_flow(ctx, id, flow, by).await
    }

    async fn discard_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        by: &str,
    ) -> Result<Flow, Error> {
        self.inner.discard_flow(ctx, id, flow, by).await
    }

    async fn validate_board(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Vec<BoardIssue>, Error> {
        self.inner.validate_board(ctx, id, flow).await
    }

    async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        self.inner.baseline_graph(ctx, id).await
    }

    async fn change_graph(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        self.inner.change_graph(ctx, id, flow).await
    }

    async fn change_view(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        level: &str,
    ) -> Result<Baseline, Error> {
        self.inner.change_view(ctx, id, flow, level).await
    }

    async fn open_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenOptionRequest,
    ) -> Result<Flow, Error> {
        self.inner.open_option(ctx, id, in_).await
    }

    async fn activate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
    ) -> Result<String, Error> {
        self.inner.activate_option(ctx, id, option, by).await
    }

    async fn evaluate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
        comment: &str,
    ) -> Result<Flow, Error> {
        self.inner
            .evaluate_option(ctx, id, option, by, comment)
            .await
    }

    async fn options(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Flow>, Error> {
        self.inner.options(ctx, id).await
    }

    async fn compare_options(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        level: &str,
        all: bool,
    ) -> Result<OptionComparison, Error> {
        self.inner.compare_options(ctx, id, level, all).await
    }

    async fn open_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenDecisionRequest,
    ) -> Result<DecisionPoint, Error> {
        self.inner.open_decision(ctx, id, in_).await
    }

    async fn rule_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: RuleRequest,
    ) -> Result<DecisionPoint, Error> {
        self.inner.rule_decision(ctx, id, in_).await
    }

    async fn answer_question(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        question: &str,
        answer: &str,
        process: &str,
        by: &str,
    ) -> Result<DecisionPoint, Error> {
        self.inner
            .answer_question(ctx, id, question, answer, process, by)
            .await
    }

    async fn ratify_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        point: &str,
        accept: bool,
        by: &str,
        comment: &str,
    ) -> Result<DecisionPoint, Error> {
        self.inner
            .ratify_decision(ctx, id, point, accept, by, comment)
            .await
    }

    async fn decision_points(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<DecisionPoint>, Error> {
        self.inner.decision_points(ctx, id).await
    }

    async fn branch(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch, Error> {
        self.inner.branch(ctx, namespace, name).await
    }

    async fn plan_merge(
        &self,
        ctx: &Ctx,
        namespace: &str,
        from: &str,
        into: &str,
    ) -> Result<MergePlan, Error> {
        self.inner.plan_merge(ctx, namespace, from, into).await
    }

    async fn rebase_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<RebaseState, Error> {
        self.inner.rebase_state(ctx, id).await
    }

    async fn baselines(&self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>, Error> {
        self.inner.baselines(ctx, namespace).await
    }

    async fn append_log(&self, ctx: &Ctx, entries: Vec<LogEntry>) -> Result<(), Error> {
        self.inner.append_log(ctx, entries).await
    }

    async fn change_log(
        &self,
        ctx: &Ctx,
        f: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>), Error> {
        self.inner.change_log(ctx, f).await
    }

    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline, Error> {
        self.inner.branch_head(ctx, namespace, name).await
    }

    async fn list_changes(&self, ctx: &Ctx, f: ChangesFilter) -> Result<Vec<Change>, Error> {
        self.inner.list_changes(ctx, f).await
    }

    async fn structures(&self, ctx: &Ctx) -> Result<Structures, Error> {
        self.inner.structures(ctx).await
    }

    async fn admin_only_type(&self, ctx: &Ctx, typ: &str) -> Result<bool, Error> {
        self.inner.admin_only_type(ctx, typ).await
    }
}
