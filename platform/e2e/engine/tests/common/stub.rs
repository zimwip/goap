//! A graph port for the tests that stub a few operations of the graph (Go embedded a nil GraphPort in a struct and
//! overrode some methods): the operations of `Stub` delegate to it, the others refuse (copied from the engine's
//! NullGraph fake).

use std::collections::BTreeMap;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Blackboard, BoardIssue, Branch, Change, ChangeID, ChangeImpact,
    ChangeImpactID, ChangeItem, DecisionPoint, Flow, Link, LinkID, LogEntry, LogFilter, Node,
    NodeID, NodeReview, ReviewBatch, Structures,
};
use goap_graphapi::{
    ChangeDependencies, ChangePatch, ChangesFilter, Error, LinkWrite, MergeInput, MergePlan,
    NewChange, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate, OpenDecisionRequest,
    OpenFlowRequest, OpenOptionRequest, OptionComparison, RebaseState, Resolution, Restructured,
    RuleRequest, SplitInput,
};

const NO_GRAPH: &str = "no graph in a stub: invalid";

/// Stub is what a test overrides; the defaults refuse (append_log accepts).
#[allow(unused_variables)]
#[async_trait]
pub trait Stub: Send + Sync {
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }
    async fn apply(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        baseline_name: &str,
    ) -> Result<Baseline, Error> {
        Err(Error::invalid(NO_GRAPH))
    }
    async fn branch(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch, Error> {
        Err(Error::invalid(NO_GRAPH))
    }
    async fn plan_merge(
        &self,
        ctx: &Ctx,
        namespace: &str,
        from: &str,
        into: &str,
    ) -> Result<MergePlan, Error> {
        Err(Error::invalid(NO_GRAPH))
    }
    async fn integrate_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: BTreeMap<NodeID, Resolution>,
    ) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }
    async fn rebase_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<RebaseState, Error> {
        Err(Error::invalid(NO_GRAPH))
    }
    async fn append_log(&self, ctx: &Ctx, entries: Vec<LogEntry>) -> Result<(), Error> {
        Ok(())
    }
}

/// StubGraph is the graph port over a stub.
pub struct StubGraph<S>(pub S);

#[allow(unused_variables)]
#[async_trait::async_trait]
impl<S: Stub> goap_graphapi::Port for StubGraph<S> {
    async fn create_change(&self, ctx: &Ctx, in_: NewChange) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn update_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        p: ChangePatch,
    ) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn move_change(&self, ctx: &Ctx, id: &ChangeID, project: &str) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn attach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn detach_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn set_change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        deps: Vec<ChangeID>,
    ) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<ChangeDependencies, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn upgrade_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        version: &str,
    ) -> Result<Change, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        self.0.change(ctx, id).await
    }

    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn propose_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        nodes: Vec<ChangeImpact>,
    ) -> Result<Vec<ChangeImpact>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCreate,
    ) -> Result<ChangeImpact, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_checkout(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCheckout,
    ) -> Result<ChangeImpact, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        in_: NodeUpdate,
    ) -> Result<ChangeImpact, Error> {
        Err(Error::invalid(NO_GRAPH))
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
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_link_delete(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_transition(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeTransition,
    ) -> Result<ChangeImpact, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_cancel(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn withdraw_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn withdraw_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_merge(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: MergeInput,
    ) -> Result<Restructured, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_split(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: SplitInput,
    ) -> Result<Restructured, Error> {
        Err(Error::invalid(NO_GRAPH))
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
        Err(Error::invalid(NO_GRAPH))
    }

    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn reopen_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        comment: &str,
    ) -> Result<Vec<ChangeImpactID>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn blackboard(&self, ctx: &Ctx, id: &ChangeID) -> Result<Blackboard, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn blackboard_in(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Blackboard, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn open_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenFlowRequest,
    ) -> Result<Flow, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn adopt_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        by: &str,
    ) -> Result<Flow, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn discard_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        by: &str,
    ) -> Result<Flow, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn validate_board(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Vec<BoardIssue>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn change_graph(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn change_view(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        level: &str,
    ) -> Result<Baseline, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn open_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenOptionRequest,
    ) -> Result<Flow, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn activate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
    ) -> Result<String, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn evaluate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
        comment: &str,
    ) -> Result<Flow, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn options(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Flow>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn compare_options(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        level: &str,
        all: bool,
    ) -> Result<OptionComparison, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn open_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenDecisionRequest,
    ) -> Result<DecisionPoint, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn rule_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: RuleRequest,
    ) -> Result<DecisionPoint, Error> {
        Err(Error::invalid(NO_GRAPH))
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
        Err(Error::invalid(NO_GRAPH))
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
        Err(Error::invalid(NO_GRAPH))
    }

    async fn decision_points(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<DecisionPoint>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn apply(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        baseline_name: &str,
    ) -> Result<Baseline, Error> {
        self.0.apply(ctx, id, baseline_name).await
    }

    async fn branch(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch, Error> {
        self.0.branch(ctx, namespace, name).await
    }

    async fn plan_merge(
        &self,
        ctx: &Ctx,
        namespace: &str,
        from: &str,
        into: &str,
    ) -> Result<MergePlan, Error> {
        self.0.plan_merge(ctx, namespace, from, into).await
    }

    async fn integrate_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: BTreeMap<NodeID, Resolution>,
    ) -> Result<Change, Error> {
        self.0.integrate_change(ctx, id, resolutions).await
    }

    async fn rebase_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<RebaseState, Error> {
        self.0.rebase_state(ctx, id).await
    }

    async fn baselines(&self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn append_log(&self, ctx: &Ctx, entries: Vec<LogEntry>) -> Result<(), Error> {
        self.0.append_log(ctx, entries).await
    }

    async fn change_log(
        &self,
        ctx: &Ctx,
        f: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>), Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn list_changes(&self, ctx: &Ctx, f: ChangesFilter) -> Result<Vec<Change>, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn structures(&self, ctx: &Ctx) -> Result<Structures, Error> {
        Err(Error::invalid(NO_GRAPH))
    }

    async fn admin_only_type(&self, ctx: &Ctx, typ: &str) -> Result<bool, Error> {
        Err(Error::invalid(NO_GRAPH))
    }
}
