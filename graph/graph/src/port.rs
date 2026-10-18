//! The graph as its callers see it: `*graph.Graph` satisfies `graphapi.Port` structurally in Go; here the graph
//! implements `goap_graphapi::Port` by delegating to its inherent methods.

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Blackboard, BoardIssue, Branch, Change, ChangeID, ChangeImpact,
    ChangeImpactID, ChangeItem, DecisionPoint, Flow, Link, LinkID, LogEntry, LogFilter, Node,
    NodeID, NodeReview, ReviewBatch, Structures,
};
use goap_graphapi::{
    ChangeDependencies, ChangePatch, ChangesFilter, LinkWrite, MergeInput, MergePlan, NewChange,
    NodeCheckout, NodeCreate, NodeTransition, NodeUpdate, OpenDecisionRequest, OpenFlowRequest,
    OpenOptionRequest, OptionComparison, Port, RebaseState, Resolution, Restructured, RuleRequest,
    SplitInput,
};

use crate::{Graph, Result};

#[async_trait::async_trait]
impl Port for Graph {
    async fn create_change(&self, ctx: &Ctx, in_: NewChange) -> Result<Change> {
        Graph::create_change(self, ctx, in_).await
    }
    async fn update_change(&self, ctx: &Ctx, id: &ChangeID, p: ChangePatch) -> Result<Change> {
        Graph::update_change(self, ctx, id, &p).await
    }
    async fn move_change(&self, ctx: &Ctx, id: &ChangeID, project: &str) -> Result<Change> {
        Graph::move_change(self, ctx, id, project).await
    }
    async fn attach_change(&self, ctx: &Ctx, id: &ChangeID, parent: &ChangeID) -> Result<Change> {
        Graph::attach_change(self, ctx, id, parent).await
    }
    async fn detach_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        Graph::detach_change(self, ctx, id).await
    }
    async fn set_change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        deps: Vec<ChangeID>,
    ) -> Result<Change> {
        Graph::set_change_dependencies(self, ctx, id, &deps).await
    }
    async fn change_dependencies(&self, ctx: &Ctx, id: &ChangeID) -> Result<ChangeDependencies> {
        Graph::change_dependencies(self, ctx, id).await
    }
    async fn upgrade_change(&self, ctx: &Ctx, id: &ChangeID, version: &str) -> Result<Change> {
        Graph::upgrade_change(self, ctx, id, version).await
    }
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        Graph::change(self, ctx, id).await
    }
    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>> {
        Graph::add_items(self, ctx, id, items).await
    }
    async fn propose_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        nodes: Vec<ChangeImpact>,
    ) -> Result<Vec<ChangeImpact>> {
        Graph::propose_impact(self, ctx, id, nodes).await
    }
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCreate,
    ) -> Result<ChangeImpact> {
        Graph::impact_node_create(self, ctx, id, in_).await
    }
    async fn impact_node_checkout(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCheckout,
    ) -> Result<ChangeImpact> {
        Graph::impact_node_checkout(self, ctx, id, in_).await
    }
    async fn impact_node_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        in_: NodeUpdate,
    ) -> Result<ChangeImpact> {
        Graph::impact_node_update(self, ctx, id, impact, in_).await
    }
    async fn impact_link_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        l: LinkWrite,
        flow: &str,
        execution: &str,
    ) -> Result<Link> {
        Graph::impact_link_create(self, ctx, id, impact, l, flow, execution).await
    }
    async fn impact_link_delete(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        Graph::impact_link_delete(self, ctx, id, link, flow, execution).await
    }
    async fn impact_node_transition(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeTransition,
    ) -> Result<ChangeImpact> {
        Graph::impact_node_transition(self, ctx, id, in_).await
    }
    async fn impact_node_cancel(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact> {
        Graph::impact_node_cancel(self, ctx, id, impact, flow, execution).await
    }
    async fn withdraw_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        Graph::withdraw_impact(self, ctx, id, impact, flow, execution).await
    }
    async fn withdraw_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        Graph::withdraw_impacts(self, ctx, id, &impacts, flow, execution).await
    }
    async fn impact_node_merge(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: MergeInput,
    ) -> Result<Restructured> {
        Graph::impact_node_merge(self, ctx, id, in_).await
    }
    async fn impact_node_split(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: SplitInput,
    ) -> Result<Restructured> {
        Graph::impact_node_split(self, ctx, id, in_).await
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
    ) -> Result<ChangeImpact> {
        Graph::impact_node_review_on(self, ctx, id, flow, execution, node, status, by, comment)
            .await
    }
    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>> {
        Graph::impact_node_review_batch(self, ctx, id, b).await
    }
    async fn reopen_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        comment: &str,
    ) -> Result<Vec<ChangeImpactID>> {
        Graph::reopen_impacts(self, ctx, id, &impacts, comment).await
    }
    async fn blackboard(&self, ctx: &Ctx, id: &ChangeID) -> Result<Blackboard> {
        Graph::blackboard(self, ctx, id).await
    }
    async fn blackboard_in(&self, ctx: &Ctx, id: &ChangeID, flow: &str) -> Result<Blackboard> {
        Graph::blackboard_in(self, ctx, id, flow).await
    }
    async fn open_flow(&self, ctx: &Ctx, id: &ChangeID, in_: OpenFlowRequest) -> Result<Flow> {
        Graph::open_flow(self, ctx, id, in_).await
    }
    async fn adopt_flow(&self, ctx: &Ctx, id: &ChangeID, flow: &str, by: &str) -> Result<Flow> {
        Graph::adopt_flow(self, ctx, id, flow, by).await
    }
    async fn discard_flow(&self, ctx: &Ctx, id: &ChangeID, flow: &str, by: &str) -> Result<Flow> {
        Graph::discard_flow(self, ctx, id, flow, by).await
    }
    async fn validate_board(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Vec<BoardIssue>> {
        Graph::validate_board(self, ctx, id, flow).await
    }
    async fn baseline_graph(&self, ctx: &Ctx, id: &BaselineID) -> Result<(Vec<Node>, Vec<Link>)> {
        Graph::baseline_graph(self, ctx, id).await
    }
    async fn change_graph(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<(Vec<Node>, Vec<Link>)> {
        Graph::change_graph(self, ctx, id, flow).await
    }
    async fn change_view(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        level: &str,
    ) -> Result<Baseline> {
        Graph::change_view(self, ctx, id, flow, level).await
    }
    async fn open_option(&self, ctx: &Ctx, id: &ChangeID, in_: OpenOptionRequest) -> Result<Flow> {
        Graph::open_option(self, ctx, id, in_).await
    }
    async fn activate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
    ) -> Result<String> {
        Graph::activate_option(self, ctx, id, option, by).await
    }
    async fn evaluate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
        comment: &str,
    ) -> Result<Flow> {
        Graph::evaluate_option(self, ctx, id, option, by, comment).await
    }
    async fn options(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Flow>> {
        Graph::options(self, ctx, id).await
    }
    async fn compare_options(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        level: &str,
        all: bool,
    ) -> Result<OptionComparison> {
        Graph::compare_options(self, ctx, id, level, all).await
    }
    async fn open_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenDecisionRequest,
    ) -> Result<DecisionPoint> {
        Graph::open_decision(self, ctx, id, in_).await
    }
    async fn rule_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: RuleRequest,
    ) -> Result<DecisionPoint> {
        Graph::rule_decision(self, ctx, id, in_).await
    }
    async fn answer_question(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        question: &str,
        answer: &str,
        process: &str,
        by: &str,
    ) -> Result<DecisionPoint> {
        Graph::answer_question(self, ctx, id, question, answer, process, by).await
    }
    async fn ratify_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        point: &str,
        accept: bool,
        by: &str,
        comment: &str,
    ) -> Result<DecisionPoint> {
        Graph::ratify_decision(self, ctx, id, point, accept, by, comment).await
    }
    async fn decision_points(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<DecisionPoint>> {
        Graph::decision_points(self, ctx, id).await
    }
    async fn apply(&self, ctx: &Ctx, id: &ChangeID, baseline_name: &str) -> Result<Baseline> {
        Graph::apply(self, ctx, id, baseline_name).await
    }
    async fn branch(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch> {
        Graph::branch(self, ctx, namespace, name).await
    }
    async fn plan_merge(
        &self,
        ctx: &Ctx,
        namespace: &str,
        from: &str,
        into: &str,
    ) -> Result<MergePlan> {
        Graph::plan_merge(self, ctx, namespace, from, into).await
    }
    async fn integrate_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: BTreeMap<NodeID, Resolution>,
    ) -> Result<Change> {
        Graph::integrate_change(self, ctx, id, &resolutions).await
    }
    async fn rebase_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<RebaseState> {
        Graph::rebase_state(self, ctx, id).await
    }
    async fn baselines(&self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>> {
        Graph::baselines(self, ctx, namespace).await
    }
    async fn append_log(&self, ctx: &Ctx, entries: Vec<LogEntry>) -> Result<()> {
        Graph::append_log(self, ctx, entries).await
    }
    async fn change_log(
        &self,
        ctx: &Ctx,
        f: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>)> {
        Graph::change_log(self, ctx, &f).await
    }
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline> {
        Graph::branch_head(self, ctx, namespace, name).await
    }
    async fn list_changes(&self, ctx: &Ctx, f: ChangesFilter) -> Result<Vec<Change>> {
        Graph::list_changes(self, ctx, &f).await
    }
    async fn structures(&self, _ctx: &Ctx) -> Result<Structures> {
        Ok(Graph::structures(self))
    }
    async fn admin_only_type(&self, _ctx: &Ctx, typ: &str) -> Result<bool> {
        Ok(Graph::admin_only_type(self, typ))
    }
}

/// The graph is held as a port (`Arc<dyn Port>`).
const _: fn(&Graph) -> &dyn Port = |g| g;
