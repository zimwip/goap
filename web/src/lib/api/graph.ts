import { rpc } from './transport';
import type { Empty, Struct } from './types/common';
import type { BaselineDiff, BoardIssue, DecisionPoint, Flow, LinkWrite, MergePlan, OptionComparison, Resolution } from './types/engine';
import type { Baseline, BaselineLinksQuery, BaselineNodesQuery, Branch, Change, ChangeImpact, ChangeItem, ChangeLogQuery, ExecutionRecord, GraphNode, ImpactEvent, Link, LogEntry, NodeEdit, NodeRef, SharedNode, Tag, TypeCount } from './types/graph';

const GRAPH = 'goap.graph.v1.GraphService';

export const graph = {
  /** namespace is required: baselines are scoped to one namespace each. */
  listBaselines: (namespace: string, signal?: AbortSignal) =>
    rpc<{ namespace: string }, { baselines?: Baseline[] }>(GRAPH, 'ListBaselines', { namespace }, signal),
  getBaselineGraph: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { baseline?: Baseline; nodes?: GraphNode[]; links?: Link[]; suspectLinks?: Link[] }>(
      GRAPH,
      'GetBaselineGraph',
      { id },
      signal,
    ),
  /** A page of the nodes of a baseline (by type, text-filtered), with the node count of every type. */
  listBaselineNodes: (req: BaselineNodesQuery, signal?: AbortSignal) =>
    rpc<BaselineNodesQuery, { baseline?: Baseline; nodes?: GraphNode[]; total?: number; types?: TypeCount[] }>(
      GRAPH,
      'ListBaselineNodes',
      req,
      signal,
    ),
  /** A page of the links of a baseline (by type, text-filtered), with the link count of every type. */
  listBaselineLinks: (req: BaselineLinksQuery, signal?: AbortSignal) =>
    rpc<BaselineLinksQuery, { baseline?: Baseline; links?: Link[]; total?: number; types?: TypeCount[] }>(
      GRAPH,
      'ListBaselineLinks',
      req,
      signal,
    ),
  /** A node of a baseline with its direct neighbours (both directions). */
  getNodeNeighbourhood: (baselineId: string, nodeId: string, signal?: AbortSignal) =>
    rpc<{ baselineId: string; nodeId: string }, { node?: GraphNode; nodes?: GraphNode[]; links?: Link[]; suspectLinkIds?: string[] }>(
      GRAPH,
      'GetNodeNeighbourhood',
      { baselineId, nodeId },
      signal,
    ),
  /** The impact log of a change (ADR 0029): every operation on its change impacts, with its caller. */
  listChangeEvents: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { events?: ImpactEvent[] }>(GRAPH, 'ListChangeEvents', { changeId }, signal),
  /** The log of a change (ADR 0030), filtered on its columns; counts: entries per type without the types filter. */
  listChangeLog: (req: ChangeLogQuery, signal?: AbortSignal) =>
    rpc<ChangeLogQuery, { entries?: LogEntry[]; counts?: Record<string, number> }>(GRAPH, 'ListChangeLog', req, signal),
  /** The whole log of a change as W3C PROV-O provenance, a JSON-LD document (ADR 0057). */
  exportChangeProvenance: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { document?: string; filename?: string; mediaType?: string }>(
      GRAPH,
      'ExportChangeProvenance',
      { changeId },
      signal,
    ),
  /** The namespaces holding at least one node. */
  listNamespaces: (signal?: AbortSignal) => rpc<Empty, { namespaces?: string[] }>(GRAPH, 'ListNamespaces', {}, signal),
  listChanges: (signal?: AbortSignal) =>
    rpc<Empty, { changes?: Change[] }>(GRAPH, 'ListChanges', {}, signal),
  getChange: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { change?: Change }>(GRAPH, 'GetChange', { id }, signal),
  getBranch: (namespace: string, name: string, signal?: AbortSignal) =>
    rpc<{ namespace: string; name: string }, { branch?: Branch; head?: Baseline }>(GRAPH, 'GetBranch', { namespace, name }, signal),
  listBranches: (namespace: string, signal?: AbortSignal) =>
    rpc<{ namespace: string }, { branches?: Branch[] }>(GRAPH, 'ListBranches', { namespace }, signal),
  createBranch: (req: { namespace: string; name: string; fromBaseline: string; origin?: string; description?: string }) =>
    rpc<typeof req, { branch?: Branch }>(GRAPH, 'CreateBranch', req),
  /** open | merged | abandoned (an abandoned branch takes no change any more). */
  setBranchStatus: (namespace: string, name: string, status: string) =>
    rpc<{ namespace: string; name: string; status: string }, Empty>(GRAPH, 'SetBranchStatus', { namespace, name, status }),
  setBranchDescription: (namespace: string, name: string, description: string) =>
    rpc<{ namespace: string; name: string; description: string }, Empty>(GRAPH, 'SetBranchDescription', { namespace, name, description }),
  /** Merges a branch into another; a node changed on both sides needs a resolution (by node id). */
  mergeBranch: (req: { namespace: string; from: string; into: string; title?: string; resolutions?: Record<string, Resolution> }) =>
    rpc<typeof req, { change?: Change; baseline?: Baseline; plan?: MergePlan }>(GRAPH, 'MergeBranch', req),
  /** Names the state an applied change leaves (ADR 0056); tags are not unique. */
  tagChange: (changeId: string, name: string) =>
    rpc<{ changeId: string; name: string }, { tag?: Tag }>(GRAPH, 'TagChange', { changeId, name }),
  /** The tags matching the filter (all empty: every tag). */
  listTags: (filter: { namespace?: string; name?: string; changeId?: string } = {}) =>
    rpc<typeof filter, { tags?: Tag[] }>(GRAPH, 'ListTags', filter),
  deleteTag: (id: string) => rpc<{ id: string }, Record<string, never>>(GRAPH, 'DeleteTag', { id }),
  /** What going from a baseline to another changes, node by node. */
  diffBaselines: (from: string, to: string, signal?: AbortSignal) =>
    rpc<{ from: string; to: string }, { nodes?: BaselineDiff[] }>(GRAPH, 'DiffBaselines', { from, to }, signal),
  /** The changes that acted on a node (headers only). */
  listNodeChanges: (nodeId: string, signal?: AbortSignal) =>
    rpc<{ nodeId: string }, { changes?: Change[] }>(GRAPH, 'ListNodeChanges', { nodeId }, signal),
  /**
   * Edits a change: title, intent, goal; status 'abandoned' abandons it (its sub-changes and its branch too); data is merged
   * into its free-form data (the criticality, ADR 0075 §3: raising it is free, lowering it asks a permission).
   */
  updateChange: (id: string, patch: { title?: string; intent?: string; goal?: string; status?: string; data?: Struct }) =>
    rpc<{ id: string; title?: string; intent?: string; goal?: string; status?: string; data?: Struct }, { change?: Change }>(GRAPH, 'UpdateChange', { id, ...patch }),
  createChange: (req: {
    title: string;
    intent?: string;
    baselineId?: string;
    methodology?: string;
    namespace?: string;
    /** branch the change is merged into (default main) */
    branch?: string;
    ownBranch?: boolean;
    parentId?: string;
    /** key of the responsible unit; '@me': the personal unit of the caller (a personal change, ADR 0037) */
    ownerOrg?: string;
  }) => rpc<typeof req, { change?: Change }>(GRAPH, 'CreateChange', req),
  /** Splits a change into one sub-change per organisational unit owning impacted nodes. */
  splitChange: (changeId: string) =>
    rpc<{ changeId: string }, { changes?: Change[] }>(GRAPH, 'SplitChange', { changeId }),
  listSubChanges: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { changes?: Change[] }>(GRAPH, 'ListSubChanges', { changeId }, signal),
  /** Integrates a committed change that waits for a resolution; resolutions are by node id. */
  mergeChange: (changeId: string, resolutions: Record<string, Resolution> = {}) =>
    rpc<
      { changeId: string; resolutions: Record<string, Resolution> },
      { change?: Change }
    >(GRAPH, 'MergeChange', { changeId, resolutions }),
  /** Flow branches of a change (relaunched steps). */
  validateBoard: (changeId: string, flow = '', signal?: AbortSignal) =>
    rpc<{ changeId: string; flow: string }, { issues?: BoardIssue[] }>(GRAPH, 'ValidateBoard', { changeId, flow }, signal),
  listFlows: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { flows?: Flow[] }>(GRAPH, 'ListFlows', { changeId }, signal),
  /** Adopts an open flow branch straight on the graph (prefer engine.decideFlow when the run is known). */
  adoptFlow: (changeId: string, flow: string) =>
    rpc<{ changeId: string; flow: string }, { flow?: Flow }>(GRAPH, 'AdoptFlow', { changeId, flow }),
  /** Discards an open flow branch straight on the graph (its candidates are rejected, its graph branch abandoned). */
  discardFlow: (changeId: string, flow: string) =>
    rpc<{ changeId: string; flow: string }, { flow?: Flow }>(GRAPH, 'DiscardFlow', { changeId, flow }),
  /** Options of a change (ADR 0009 §3, ADR 0032 §6): hypotheses explored on flows of their own; the active one is
   * where every call that names no flow goes. */
  listOptions: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { options?: Flow[]; active?: string }>(GRAPH, 'ListOptions', { changeId }, signal),
  openOption: (changeId: string, name: string, hypothesis: string, activate: boolean) =>
    rpc<{ changeId: string; name: string; hypothesis: string; activate: boolean }, { option?: Flow }>(GRAPH, 'OpenOption', { changeId, name, hypothesis, activate }),
  /** Works on an option; '' or 'main': back to the main flow. */
  activateOption: (changeId: string, option: string) =>
    rpc<{ changeId: string; option: string }, { active?: string }>(GRAPH, 'ActivateOption', { changeId, option }),
  evaluateOption: (changeId: string, option: string, comment: string) =>
    rpc<{ changeId: string; option: string; comment: string }, { option?: Flow }>(GRAPH, 'EvaluateOption', { changeId, option, comment }),
  /** Selects an option: its versions join the change branch, the other open options are rejected. */
  selectOption: (changeId: string, option: string) =>
    rpc<{ changeId: string; option: string }, { option?: Flow }>(GRAPH, 'SelectOption', { changeId, option }),
  rejectOption: (changeId: string, option: string) =>
    rpc<{ changeId: string; option: string }, { option?: Flow }>(GRAPH, 'RejectOption', { changeId, option }),
  /** The nodes the options changed, each side against the main flow, at written or accepted. */
  compareOptions: (changeId: string, level: string, all = false, signal?: AbortSignal) =>
    rpc<{ changeId: string; level: string; all: boolean }, OptionComparison>(GRAPH, 'CompareOptions', { changeId, level, all }, signal),
  /** Decision points of a change (ADR 0009 §4). */
  listDecisionPoints: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { points?: DecisionPoint[] }>(GRAPH, 'ListDecisionPoints', { changeId }, signal),
  openDecision: (req: { changeId: string; question: string; allOptions: boolean; options?: string[]; criteria?: string[]; policy?: { decider?: string; threshold?: number; maxRounds?: number; maxDuration?: string } }) =>
    rpc<typeof req, { point?: DecisionPoint }>(GRAPH, 'OpenDecision', req),
  /** A ruling from the IDE is a person's: it needs no ratification. */
  ruleDecision: (req: { changeId: string; point: string; outcome: string; option?: string; confidence?: number; justification: string; questions?: string[] }) =>
    rpc<typeof req, { point?: DecisionPoint }>(GRAPH, 'RuleDecision', req),
  answerQuestion: (changeId: string, question: string, answer: string) =>
    rpc<{ changeId: string; question: string; answer: string }, { point?: DecisionPoint }>(GRAPH, 'AnswerQuestion', { changeId, question, answer }),
  ratifyDecision: (changeId: string, point: string, accept: boolean, comment: string) =>
    rpc<{ changeId: string; point: string; accept: boolean; comment: string }, { point?: DecisionPoint }>(GRAPH, 'RatifyDecision', { changeId, point, accept, comment }),
  /** The graph of a change at a level (written, accepted, landed) on a flow (ADR 0032 §5). */
  getChangeView: (changeId: string, flow: string, level: string, signal?: AbortSignal) =>
    rpc<{ changeId: string; flow: string; level: string }, { baseline?: Baseline }>(GRAPH, 'GetChangeView', { changeId, flow, level }, signal),
  /** What merging a branch into another would do. */
  planMerge: (namespace: string, from: string, into: string, signal?: AbortSignal) =>
    rpc<{ namespace: string; from: string; into: string }, { plan?: MergePlan }>(GRAPH, 'PlanMerge', { namespace, from, into }, signal),
  getSharedNodes: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { nodes?: SharedNode[] }>(GRAPH, 'GetSharedNodes', { changeId }, signal),
  /** A node version with its outgoing and incoming links (version 0: the latest). */
  getNode: (ref: NodeRef, signal?: AbortSignal) =>
    rpc<{ ref: NodeRef }, { view?: { node?: GraphNode; latest?: number; out?: Link[]; in?: Link[]; frozen?: boolean } }>(GRAPH, 'GetNode', { ref }, signal),
  /** Every version of a node, all branches. */
  listNodeVersions: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { versions?: GraphNode[] }>(GRAPH, 'ListNodeVersions', { id }, signal),
  /** The versions the change starts from (the pre version of its change impacts). */
  getChangeImpacts: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { nodes?: NodeRef[] }>(GRAPH, 'GetChangeImpacts', { changeId }, signal),
  /** Declare the nodes a change acts on (an impact: pre, intent, rationale). */
  addChangeImpacts: (changeId: string, nodes: ChangeImpact[]) =>
    rpc<{ changeId: string; nodes: ChangeImpact[] }, { nodes?: ChangeImpact[] }>(GRAPH, 'AddChangeImpacts', { changeId, nodes }),
  // The node operations of a change (ADR 0076): a node is created or checked out in a change (a working version),
  // edited in place (properties, owner, outgoing links), checked in once its review is accepted (frozen), moved along
  // its lifecycle from a checked-in version (a version of its own). flow: the flow or option written on ('main' names
  // the main flow, '' is the active option).
  /** Creates a node in a change: the impact (intent created) and its first version, checked out. */
  createNode: (changeId: string, n: { key: string; type: string; props?: Struct; owner?: string; rationale: string; links?: LinkWrite[] }, flow = '') =>
    rpc<typeof n & { changeId: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'CreateNode', { changeId, ...n, flow }),
  /** Checks out a node (by its impact, or by the node: the impact is declared) for editing: a new working version. */
  checkoutNode: (changeId: string, target: { changeImpactId?: string; nodeId?: string }, rationale = '', flow = '') =>
    rpc<{ changeId: string; changeImpactId?: string; nodeId?: string; rationale: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'CheckoutNode', { changeId, ...target, rationale, flow }),
  /** Merges properties into (and transfers the owner of) a checked-out working version, in place. */
  updateNode: (changeId: string, changeImpactId: string, u: { props?: Struct; owner?: string }, flow = '') =>
    rpc<{ changeId: string; changeImpactId: string; props?: Struct; owner?: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'UpdateNode', { changeId, changeImpactId, ...u, flow }),
  /** Adds an outgoing link to a checked-out working version, in place. */
  createLink: (changeId: string, changeImpactId: string, l: LinkWrite, flow = '') =>
    rpc<{ changeId: string; changeImpactId: string; type: string; to: NodeRef; props?: Struct; flow: string }, { link?: Link }>(GRAPH, 'CreateLink', {
      changeId,
      changeImpactId,
      type: l.type,
      to: l.to,
      props: l.props,
      flow,
    }),
  updateLink: (changeId: string, linkId: string, props: Struct, flow = '') =>
    rpc<{ changeId: string; linkId: string; props: Struct; flow: string }, { link?: Link }>(GRAPH, 'UpdateLink', { changeId, linkId, props, flow }),
  /** Removes an outgoing link of a checked-out working version (removing a child is a modification of its parent). */
  deleteLink: (changeId: string, linkId: string, flow = '') =>
    rpc<{ changeId: string; linkId: string; flow: string }, Empty>(GRAPH, 'DeleteLink', { changeId, linkId, flow }),
  /** Freezes the working version of an impact: its accepted review authorizes it. */
  checkinNode: (changeId: string, changeImpactId: string, flow = '') =>
    rpc<{ changeId: string; changeImpactId: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'CheckinNode', { changeId, changeImpactId, flow }),
  /** Moves a checked-in node along its lifecycle (by its impact, or by the node: the impact is declared). */
  transitionNode: (changeId: string, target: { changeImpactId?: string; nodeId?: string }, state: string, rationale = '', flow = '') =>
    rpc<{ changeId: string; changeImpactId?: string; nodeId?: string; state: string; rationale: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'TransitionNode', {
      changeId,
      ...target,
      state,
      rationale,
      flow,
    }),
  /** Drops the working version of an impact (a creation never checked in leaves no node). */
  cancelCheckout: (changeId: string, changeImpactId: string, flow = '') =>
    rpc<{ changeId: string; changeImpactId: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'CancelCheckout', { changeId, changeImpactId, flow }),
  /** Takes an impact out of the change (its working version is dropped); refused once a version of it is checked in. */
  removeChangeImpact: (changeId: string, changeImpactId: string, flow = '') =>
    rpc<{ changeId: string; changeImpactId: string; flow: string }, Empty>(GRAPH, 'RemoveChangeImpact', { changeId, changeImpactId, flow }),
  /** Sends accepted or rejected impacts back to proposed (a rejected one is reworked); the comment is mandatory. */
  reopenChangeImpacts: (changeId: string, changeImpactIds: string[], comment: string) =>
    rpc<{ changeId: string; changeImpactIds: string[]; comment: string }, { reopened?: string[] }>(GRAPH, 'ReopenChangeImpacts', { changeId, changeImpactIds, comment }),
  /** Accept or reject a change impact; the comment is mandatory. */
  /** flow: the flow or option the review is made on ('main' names the main flow; '' is the active option). */
  reviewChangeImpact: (changeId: string, changeImpactId: string, accept: boolean, comment: string, flow = '') =>
    rpc<{ changeId: string; changeImpactId: string; accept: boolean; comment: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'ReviewChangeImpact', { changeId, changeImpactId, accept, comment, flow }),
  /** The change as a flow or an option sees it: its change impacts (with the post versions of that flow) and items. */
  getBlackboard: (changeId: string, flow: string, signal?: AbortSignal) =>
    rpc<{ changeId: string; flow: string }, { change?: Change; options?: Flow[]; activeOption?: string; decisionPoints?: DecisionPoint[] }>(GRAPH, 'GetBlackboard', { changeId, flow }, signal),
  /** Create a change, write the edits on its branch, accept and check them in and apply it (one call). */
  commitEdits: (req: { namespace: string; title: string; intent: string; baselineId: string; methodology?: string; edits: NodeEdit[] }) =>
    rpc<typeof req, { changeId?: string }>(GRAPH, 'CommitEdits', req),
  addItems: (changeId: string, items: ChangeItem[]) =>
    rpc<{ changeId: string; items: ChangeItem[] }, { items?: ChangeItem[] }>(GRAPH, 'AddItems', { changeId, items }),
  /** Execution journal of a change, optionally restricted to given processes. */
  listExecutions: (changeId: string, processIds: string[] = [], signal?: AbortSignal) =>
    rpc<{ changeId: string; processIds?: string[] }, { records?: ExecutionRecord[] }>(
      GRAPH,
      'ListExecutions',
      processIds.length ? { changeId, processIds } : { changeId },
      signal,
    ),
  /** Removes a change that landed nothing, with its log (ADR 0037); refused once anything of it is applied or used. */
  deleteChange: (changeId: string) => rpc<{ changeId: string }, { change?: Change }>(GRAPH, 'DeleteChange', { changeId }),
  applyChange: (changeId: string, baselineName: string) =>
    rpc<{ changeId: string; baselineName: string }, { baseline?: Baseline }>(GRAPH, 'ApplyChange', {
      changeId,
      baselineName,
    }),
};
