//! The graph as its callers see it.

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Blackboard, BoardIssue, Branch, Change, ChangeID, ChangeImpact,
    ChangeImpactID, ChangeItem, DecisionPoint, Flow, Link, LinkID, LogEntry, LogFilter, Node,
    NodeID, NodeReview, ReviewBatch, Structures,
};

use crate::{
    ChangeDependencies, ChangePatch, ChangesFilter, Error, LinkWrite, MergeInput, MergePlan,
    NewChange, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate, OpenDecisionRequest,
    OpenFlowRequest, OpenOptionRequest, OptionComparison, RebaseState, Resolution, Restructured,
    RuleRequest, SplitInput,
};

/// Port is the graph as its callers see it: the operations on the changes and the reads the engine
/// (engine::GraphPort) and the built-in MCPs make. The graph implements it in-process, the graph client over RPC.
/// Every method takes the context of the call first (the principal, cancellation, deadline).
#[async_trait::async_trait]
pub trait Port: Send + Sync {
    async fn create_change(&self, ctx: &Ctx, in_: NewChange) -> Result<Change, Error>;
    async fn update_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        p: ChangePatch,
    ) -> Result<Change, Error>;
    /// MoveChange moves a root change to another project, with its open sub-changes (ADR 0091).
    async fn move_change(&self, ctx: &Ctx, id: &ChangeID, project: &str) -> Result<Change, Error>;
    /// AttachChange makes a root change a sub-change of a change whose methodology integrates its own, DetachChange
    /// gives it back (ADR 0106).
    async fn attach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change, Error>;
    async fn detach_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error>;
    /// SetChangeDependencies replaces the changes a change depends on; ChangeDependencies reads them, the changes
    /// depending on it and the merge order of its sub-changes (ADR 0108).
    async fn set_change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        deps: Vec<ChangeID>,
    ) -> Result<Change, Error>;
    async fn change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<ChangeDependencies, Error>;
    /// UpgradeChange moves an open change to another version of its methodology, the latest when version is "" (ADR
    /// 0113).
    async fn upgrade_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        version: &str,
    ) -> Result<Change, Error>;
    /// Change is the change as stored, its items of every flow included (the blackboard is a view of one flow).
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error>;
    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, Error>;
    /// Change impacts (ADR 0024, 0076, 0079): declare the nodes a change acts on, check them out, edit their draft,
    /// review and move them.
    async fn propose_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        nodes: Vec<ChangeImpact>,
    ) -> Result<Vec<ChangeImpact>, Error>;
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCreate,
    ) -> Result<ChangeImpact, Error>;
    async fn impact_node_checkout(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeCheckout,
    ) -> Result<ChangeImpact, Error>;
    async fn impact_node_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        in_: NodeUpdate,
    ) -> Result<ChangeImpact, Error>;
    async fn impact_link_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        l: LinkWrite,
        flow: &str,
        execution: &str,
    ) -> Result<Link, Error>;
    async fn impact_link_delete(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error>;
    async fn impact_node_transition(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: NodeTransition,
    ) -> Result<ChangeImpact, Error>;
    async fn impact_node_cancel(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact, Error>;
    async fn withdraw_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error>;
    /// WithdrawImpacts takes several change impacts out of the change at once, all or none.
    async fn withdraw_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error>;
    /// ImpactNodeMerge and ImpactNodeSplit replace nodes by successors from the side of their parents (ADR 0077).
    async fn impact_node_merge(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: MergeInput,
    ) -> Result<Restructured, Error>;
    async fn impact_node_split(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: SplitInput,
    ) -> Result<Restructured, Error>;
    #[allow(clippy::too_many_arguments)]
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
    ) -> Result<ChangeImpact, Error>;
    /// ImpactNodeReviewBatch applies reviews together, all or none (ADR 0080: the submission of a review).
    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, Error>;
    /// ReopenImpacts sends decided impacts back to proposed (ADR 0075: a derogation expired).
    async fn reopen_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        comment: &str,
    ) -> Result<Vec<ChangeImpactID>, Error>;
    async fn blackboard(&self, ctx: &Ctx, id: &ChangeID) -> Result<Blackboard, Error>;
    /// BlackboardIn is the blackboard of a flow branch ("" = main); OpenFlow / AdoptFlow / DiscardFlow relaunch a step
    /// on a new branch and decide it.
    async fn blackboard_in(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Blackboard, Error>;
    async fn open_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenFlowRequest,
    ) -> Result<Flow, Error>;
    async fn adopt_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        by: &str,
    ) -> Result<Flow, Error>;
    async fn discard_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        by: &str,
    ) -> Result<Flow, Error>;
    /// ValidateBoard checks the consistency of the blackboard seen from a flow.
    async fn validate_board(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Vec<BoardIssue>, Error>;
    async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), Error>;
    /// ChangeGraph is the graph a call on a change reads: the active option's (or the option flow names), else the
    /// reference baseline of the change (ADR 0032 §6).
    async fn change_graph(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<(Vec<Node>, Vec<Link>), Error>;
    /// ChangeView is the graph of a change at a level (written, accepted, landed) on a flow (ADR 0032 §5).
    async fn change_view(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        level: &str,
    ) -> Result<Baseline, Error>;
    /// Options of a change (ADR 0009 §3, ADR 0032 §6): open, activate, evaluate, list and compare them. Selecting and
    /// rejecting one is a decision, made through the graph service.
    async fn open_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenOptionRequest,
    ) -> Result<Flow, Error>;
    async fn activate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
    ) -> Result<String, Error>;
    async fn evaluate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
        comment: &str,
    ) -> Result<Flow, Error>;
    async fn options(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Flow>, Error>;
    async fn compare_options(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        level: &str,
        all: bool,
    ) -> Result<OptionComparison, Error>;
    /// Decision points (ADR 0009 §4): the item operations of type decisionPoint and decision.investigate go through
    /// them.
    async fn open_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: OpenDecisionRequest,
    ) -> Result<DecisionPoint, Error>;
    async fn rule_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: RuleRequest,
    ) -> Result<DecisionPoint, Error>;
    #[allow(clippy::too_many_arguments)]
    async fn answer_question(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        question: &str,
        answer: &str,
        process: &str,
        by: &str,
    ) -> Result<DecisionPoint, Error>;
    #[allow(clippy::too_many_arguments)]
    async fn ratify_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        point: &str,
        accept: bool,
        by: &str,
        comment: &str,
    ) -> Result<DecisionPoint, Error>;
    async fn decision_points(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<DecisionPoint>, Error>;
    async fn apply(&self, ctx: &Ctx, id: &ChangeID, baseline_name: &str)
    -> Result<Baseline, Error>;
    /// Branch, PlanMerge and IntegrateChange are the second step of Apply: the merge of a committed change into the
    /// branch it was forked from, with the resolutions of the conflicts a person settled (ADR 0056).
    async fn branch(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch, Error>;
    async fn plan_merge(
        &self,
        ctx: &Ctx,
        namespace: &str,
        from: &str,
        into: &str,
    ) -> Result<MergePlan, Error>;
    async fn integrate_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: BTreeMap<NodeID, Resolution>,
    ) -> Result<Change, Error>;
    /// RebaseState tells where a change stands against what it builds on: the conflicts a rebase left to settle (ADR
    /// 0101).
    async fn rebase_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<RebaseState, Error>;
    async fn baselines(&self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>, Error>;
    /// AppendLog / ChangeLog are the log of the changes, where the execution journal lives (ADR 0011, goap-journal).
    async fn append_log(&self, ctx: &Ctx, entries: Vec<LogEntry>) -> Result<(), Error>;
    /// ChangeLog returns the entries matching the filter, with the counts of the graph (by name).
    async fn change_log(
        &self,
        ctx: &Ctx,
        f: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>), Error>;
    /// BranchHead gives the head of a branch: the state the last change left, the empty state (empty id) before any.
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline, Error>;
    /// ListChanges lists the changes matching a filter (goap-change.list): which open change a request continues,
    /// else a new one is proposed.
    async fn list_changes(&self, ctx: &Ctx, f: ChangesFilter) -> Result<Vec<Change>, Error>;
    /// Structures are the organisation and project hierarchies in force (ADR 0054); AdminOnlyType tells a node type
    /// platform administrators alone write (ADR 0068).
    async fn structures(&self, ctx: &Ctx) -> Result<Structures, Error>;
    async fn admin_only_type(&self, ctx: &Ctx, typ: &str) -> Result<bool, Error>;
}

/// The ports are held as trait objects (`Arc<dyn Port>`).
const _: Option<&dyn Port> = None;
