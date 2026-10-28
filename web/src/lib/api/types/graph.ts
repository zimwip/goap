// Graph types (proto3 JSON).

import type { Int64, Struct } from './common';
import type { ChangeStackJson, StackChangeJson, VersionStepJson, BaselineStepJson, GraphStatsJson, NamespaceSummaryJson, DistributionJson, NodeTypeStatsJson, LinkTypeStatsJson, ViewJson } from '../../../gen/goap/graph/v1/graph_pb';

// --- graph ------------------------------------------------------------------

export interface NodeRef {
  id?: string;
  version?: number;
}

export interface GraphNode {
  id?: string;
  namespace?: string;
  version?: number;
  key?: string;
  type?: string;
  props?: Struct;
  deleted?: boolean;
  changeId?: string;
  createdAt?: string;
  /** lifecycle state of the version ('' : the type has none) */
  state?: string;
  branch?: string;
  /** versions this one descends from (two for a merge) */
  parents?: number[];
  /** create | revise | derive | merge */
  reason?: string;
  /** change impact that produced this version, and why it was accepted (ADR 0024) */
  changeImpact?: string;
  comment?: string;
  /** action run that wrote this version (what a relaunch marks stale) */
  execution?: string;
  /** the branches the version joined besides the one it was written on (ADR 0032; filled by listNodeVersions) */
  joined?: string[];
  /** the organisational unit responsible for the version and the project the node was created in (ADR 0054): logical URLs with no version, lnk:<namespace>/<type>/<key> (ADR 0154 §9; refKey gives the key) */
  responsible?: string;
  project?: string;
  /** this view is the draft a change holds of the node (version 0): the node has no version until the change commits (ADR 0079) */
  draft?: boolean;
  /** the nodes this one derives from (ADR 0077): set on the first version of the successor of a merge or a split */
  origins?: NodeRef[];
  /** who created the node (ADR 0161 §3): the subject, and the unit it was created in (a logical URL); the same for every version */
  creator?: string;
  creatorUnit?: string;
  /** the person whose edits made the version (attribution) */
  editor?: string;
  /** how far the version is seen, stamped at commit: personal | private | project | tree | public (ADR 0161 §4) */
  visibility?: string;
}

export interface Link {
  id?: string;
  type?: string;
  from?: NodeRef;
  /** the node version of this graph; unset for a link to another source */
  to?: NodeRef;
  props?: Struct;
  changeId?: string;
  /** the logical URL of a target in another source (ADR 0154), `to` unset */
  target?: string;
}

/** An entry of the log of a change (ADR 0030): a fact, a journal record or an impact event. */
export interface LogEntry {
  /** position in the log (int64: a string in JSON) */
  seq?: Int64;
  id?: string;
  changeId?: string;
  /** <stream>.<kind>: fact.artifact, journal.schedule, impact.transitioned… */
  type?: string;
  /** flow branch ('' = the main flow) */
  flow?: string;
  processId?: string;
  execution?: string;
  subject?: string;
  by?: string;
  at?: string;
  /** the whole fact, journal record or impact event, as JSON */
  payload?: string;
  /** the id of the entry this one follows from (ADR 0105): a verdict names the submitted review that made it */
  causation?: string;
}

/** The request and the answer of one LLM call (payload of a model.call log entry). */
export interface ModelExchange {
  step?: number;
  call?: number;
  system?: string;
  messages?: { role?: string; content?: string }[];
  response?: string;
  truncated?: boolean;
  /** the global behaviours the gateway added to `system` (ADR 0093); a leading `!` marks one dropped by the cap */
  behaviors?: string[];
  /** estimated tokens they added */
  behaviorTokens?: number;
  /** `behaviorTokens` is the byte estimate, not a measured cost */
  behaviorsEstimated?: boolean;
}

export interface ChangeLogQuery {
  changeId: string;
  /** exact types or streams ('journal.') */
  types?: string[];
  /** flow branches; 'main' is the main flow */
  flows?: string[];
  processIds?: string[];
  execution?: string;
  afterSeq?: number;
  limit?: number;
}

/** What a rebase did to an impact of a change (ADR 0101, 0103). */
export interface RebasedImpact {
  changeImpactId?: string;
  key?: string;
  /** the merged draft differs from the change's: its review went back to proposed */
  changed?: boolean;
  /** the draft took values from the target branch */
  merged?: boolean;
  /** the fields changed on both sides: props.<key>, owner, state, link:<type>:<node>, node */
  conflicts?: string[];
}

/** The conflicts a rebase left on an impact, not settled yet. */
export interface ImpactConflicts {
  changeImpactId?: string;
  key?: string;
  conflicts?: string[];
}

/** What a change can commit, merge and close (ADR 0102, 0166), as the server judges it: the one source of the Commit,
 * Merge and Close buttons. */
export interface CommitState {
  /** keys of the accepted modifications the next commit writes */
  toCommit?: string[];
  /** nodes withdrawn after their commit that the next commit reverts */
  reverts?: number;
  /** what holds the close */
  unsettled?: string[];
  /** the change committed on its branch something it has not merged yet */
  commits?: boolean;
  canCommit?: boolean;
  /** the change committed nodes it has not merged yet (unmerged of them) */
  canMerge?: boolean;
  /** what the next commit may write, node by node: the choices of a partial commit */
  candidates?: CommitCandidate[];
  unmerged?: number;
  /** nothing holds the close of the change */
  canClose?: boolean;
}

/** A node the next commit of a change may write. */
export interface CommitCandidate {
  nodeId?: string;
  key?: string;
  type?: string;
  /** created, modified or reverted (withdrawn since its commit) */
  kind?: string;
  /** the change impact that holds it; none for a revert */
  changeImpactId?: string;
}

/** Where a change stands against the head of the branch it was forked from (ADR 0101, 0103). */
export interface RebaseState {
  /** the parent change ('' when the change is not a sub-change) */
  parentId?: string;
  /** the impacts whose draft the parent changed since the sub-change took it */
  behindImpactIds?: string[];
  conflicts?: ImpactConflicts[];
  /** a root change behind the head of its target branch (ADR 0101): the branch and the head a rebase moves it to */
  branch?: string;
  onto?: string;
}

/** An operation on the change impacts of a change (ADR 0029). */
export interface ImpactEvent {
  id?: string;
  changeId?: string;
  seq?: number;
  /** empty for a change-level event (adopted) */
  impactId?: string;
  /** proposed: the impact of an existing node; created: a new node, its impact and draft in one event; checkedOut: the draft of an existing node (ADR 0079) */
  op?: 'proposed' | 'created' | 'checkedOut' | 'transitioned' | 'updated' | 'reviewed' | 'discarded' | 'cancelled' | 'withdrawn' | 'adopted' | 'committed' | 'rebased' | 'requalified' | string;
  /** the caller: flow branch ('' = main flow), journal record of the action run, principal or component */
  flow?: string;
  execution?: string;
  by?: string;
  at?: string;
  state?: ChangeImpact;
  post?: NodeRef;
  pre?: NodeRef;
  committed?: NodeRef;
  review?: NodeReview;
  stale?: string[];
  /** committed: the branch the version committed on (the change's own at commit, the target at integration) */
  branch?: string;
  /** updated: what changed in place ({props, owner, addLink, ...}); created: {origins: [{id, version, key}]} for the successor of a merge or a split (ADR 0077) */
  patch?: Record<string, unknown>;
}

/** What a read of the current graph is scoped to (ADR 0157): a lasting branch (`namespace`, `branch`, empty: main) at one
 * of its baselines (empty: its head; a baseline alone names its namespace), or a change on a flow (`changeId`, `flow`).
 * The graph refuses a read of the current graph naming none; only the reads of the history name versions. */
export type GraphView = ViewJson;

/** What a read query answered (ADR 0160, RPC QueryView): the columns, the rows (JSON values; a node, a link and a path
 * are objects naming them, `{$node: id}`, `{$link: id}`, `{$path: {nodes, links}}`), the nodes and links they name,
 * whether a cap stopped the match and why. */
export interface QueryResult {
  columns?: string[];
  rows?: unknown[][];
  nodes?: GraphNode[];
  links?: Link[];
  truncated?: boolean;
  notes?: string[];
  reads?: number;
  baselineId?: string;
}

export interface BaselineNodesQuery {
  /** the view paged: a baseline, or a change with what it wrote laid over its fork */
  view: GraphView;
  /** qualified node type; empty: every type */
  type?: string;
  /** matched against the key, the type and the string properties */
  query?: string;
  offset?: number;
  /** page size (max 500) */
  limit?: number;
  includeDeleted?: boolean;
  /** the nodes the impacts of this change act on, at their version in the baseline (ADR 0140) */
  changeId?: string;
}

export interface BaselineLinksQuery {
  baselineId: string;
  /** link type; empty: every type */
  type?: string;
  /** matched against the type and the string properties */
  query?: string;
  offset?: number;
  /** page size (max 500) */
  limit?: number;
}

/** A node and its links as GetNode answers them: a page of each direction (by type then id), with the number of links in
 * each and per type; the rest is read with listNodeLinks (graph.getNodeWithLinks reads them all). */
export interface NodeView {
  node?: GraphNode;
  latest?: number;
  out?: Link[];
  in?: Link[];
  notCommittable?: boolean;
  outTotal?: number;
  inTotal?: number;
  outTypes?: TypeCount[];
  inTypes?: TypeCount[];
}

export interface TypeCount {
  type?: string;
  count?: number;
}

export interface Baseline {
  id?: string;
  name?: string;
  parentId?: string;
  /** the head of the branch merged in, when this baseline is the result of a merge (a second parent) */
  mergedFrom?: string;
  changeId?: string;
  nodes?: Record<string, number>;
  createdAt?: string;
  branch?: string;
  /** the namespace this baseline snapshots (a baseline holds one namespace's nodes) */
  namespace?: string;
  /** commit | merge | fast-forward | snapshot (ADR 0056) */
  kind?: string;
  /** the baseline the change was forked from before its rebase onto the head of its target (ADR 0101) */
  rebasedFrom?: string;
}

/** A name given to one exact node version (ADR 0143 §6); not unique. */
export interface Tag {
  id?: string;
  name?: string;
  /** the namespace of the node */
  namespace?: string;
  nodeId?: string;
  version?: number;
  by?: string;
  createdAt?: string;
}

export interface Branch {
  name?: string;
  namespace?: string;
  parent?: string;
  forkBaseline?: string;
  head?: string;
  origin?: string;
  status?: string;
  createdAt?: string;
  description?: string;
  /** the first fork baseline of a change branch moved by a rebase (ADR 0101) */
  rebasedFrom?: string;
}

export interface Decision {
  item?: string;
  accept?: boolean;
  comment?: string;
}

export type ItemType = 'decision' | 'artifact' | 'flow';

/** An item type a change may hold, declared by a domain in force (ADR 0128); `permission` / `subjectField` what writing
 * one asks of its writer. */
export interface ItemTypeInfo {
  type?: string;
  /** the namespace of the domain declaring it, and its version */
  domain?: string;
  domainVersion?: string;
  description?: string;
  permission?: string;
  subjectField?: string;
  /** the platform code gives its items a meaning beyond their data */
  code?: boolean;
}

/** Status of a superseded item (rebase, merge): see its replacement's `supersedes`. */
export const ITEM_SUPERSEDED = 'superseded';

export interface ChangeItem {
  id?: string;
  type?: ItemType | string;
  target?: string;
  status?: string;
  /** flow branch that produced the item (empty: the main flow); flowEvent on type 'flow' */
  flow?: string;
  flowEvent?: FlowEvent;
  /** event of a decision point on type 'decision_point' (ADR 0009 §4) */
  decisionEvent?: { op?: string; point?: string; outcome?: string; confidence?: number; policy?: DecisionPolicy; human?: boolean; accept?: boolean };
  decision?: Decision;
  data?: Struct;
  producedBy?: string;
  derivedFrom?: string[];
  createdAt?: string;
  /** items superseded by this one (rebase, merge) */
  supersedes?: string[];
  /** execution journal record that produced the item */
  execution?: string;
}

/** A methodology of the guardian chain of a change, at the version it was saved with (ADR 0106). */
export interface GuardianRef {
  methodology?: string;
  version?: string;
}

/** A change related to another by a dependency, as the dependent change sees it (ADR 0108). */
export interface Dependency {
  id?: string;
  title?: string;
  methodology?: string;
  status?: string;
  state?: string;
  parentId?: string;
  /** the work of the dependency has reached the branch the dependent change merges into */
  satisfied?: boolean;
  /** why not */
  reason?: string;
}

/** The dependencies around a change (ADR 0108). */
export interface ChangeDependencies {
  /** the changes it depends on */
  dependsOn?: Dependency[];
  /** the open changes depending on it; satisfied: its work reached the branch each merges into */
  dependents?: Dependency[];
  /** its open sub-changes in the order they merge; satisfied: their own dependencies are */
  order?: Dependency[];
}

export interface Change {
  id?: string;
  namespace?: string;
  /** branch the change works on (change-<id> when it has its own) */
  branch?: string;
  /** the version of its methodology the change follows: the latest published when it was created, until upgraded (ADR 0113) */
  methodologyVersion?: string;
  /** position of the log of the change its impacts and items were read at, the view held (ADR 0153; int64: a string in JSON) */
  seq?: Int64;
  /** the methodologies framing the change, outermost first, its own last (ADR 0106) */
  guardians?: GuardianRef[];
  /** the changes whose work reaches the branch it merges into before it merges (ADR 0108) */
  dependsOn?: string[];
  /** sub-change: parent change; the responsible unit, a logical URL (ADR 0154 §9) */
  parentId?: string;
  ownerOrg?: string;
  /** the project the change acts in (ADR 0054), a logical URL (ADR 0154 §9) */
  projectId?: string;
  title?: string;
  intent?: string;
  methodology?: string;
  goal?: string;
  status?: 'draft' | 'active' | 'committed' | 'applied' | 'abandoned' | string;
  baselineId?: string;
  resultBaselineId?: string;
  /** the domain lifecycle the change follows (named by its methodology) and its state (ADR 0058); absent: none */
  lifecycle?: string;
  state?: string;
  data?: Struct;
  items?: ChangeItem[];
  /** the nodes the change reads, modifies or creates: stored, and derived from its items */
  nodes?: ChangeImpact[];
  createdAt?: string;
}

export interface NodeReview {
  status?: 'accepted' | 'rejected' | string;
  by?: string;
  comment?: string;
  at?: string;
  /** flow branch the review was made on, and whether an adopted flow replaced it */
  flow?: string;
  superseded?: boolean;
  /** the review object (ADR 0080) the review was submitted in; empty for a review made on its own */
  reviewId?: string;
}

/** An entry of a review object (ADR 0080): the review of one change impact. */
export interface ReviewEntry {
  changeImpactId: string;
  comment?: string;
  /** accept | reject; empty while undecided */
  outcome?: 'accept' | 'reject' | '' | string;
}

/** A review object of a change (ADR 0080): a global comment and one entry per change impact, built up while open, then
 * submitted (every impact reviewed at once) or discarded; both final. */
export interface ReviewRecord {
  key: string;
  /** flow the entries are reviewed on (empty: the main flow) */
  flow?: string;
  comment?: string;
  status: 'open' | 'submitted' | 'discarded' | string;
  entries?: ReviewEntry[];
  by?: string;
  submittedAt?: string;
  item?: string;
  versions?: number;
}

/** A change to an open review: comment replaces the global one when set; remove then add change the impacts; entries
 * set the comment / outcome of entries once the set changed. */
export interface ReviewEdit {
  comment?: string;
  add?: string[];
  remove?: string[];
  entries?: { changeImpactId: string; comment?: string; outcome?: string }[];
}

/** The link from a change to a node (ADR 0024). */
/** A node written by a commit: created (key, type) or modified (pre); state moves it along its lifecycle (a node is
 * never deleted: one no parent holds is retired by its lifecycle, ADR 0076). */
export interface NodeEdit {
  key?: string;
  type?: string;
  pre?: NodeRef;
  props?: Struct;
  state?: string;
  rationale?: string;
  links?: { type: string; to?: NodeRef; toKey?: string; props?: Struct }[];
  removeLinks?: string[];
  /** key of the organisational unit the node goes to (ADR 0054); unset: unchanged, or the unit holding the commit */
  responsible?: string;
}

export interface ImpactValidation {
  valid: boolean;
  message?: string;
  /** the draft differs from the version it was checked out from (or creates a node): only a modification is accepted
   * and committed (ADR 0102) */
  modified?: boolean;
}

export interface ChangeImpact {
  id?: string;
  key?: string;
  type?: string;
  intent?: 'created' | 'modified' | string;
  rationale?: string;
  /** released version the change starts from (none for a created node) */
  pre?: NodeRef;
  /** the draft the change holds of the node: a draft reference `{id, version: 0}` (ADR 0079; see isDraft); none while the impact of an existing node is only proposed */
  post?: NodeRef;
  /** version on the target branch once applied */
  committed?: NodeRef;
  review?: 'proposed' | 'accepted' | 'rejected' | string;
  reviews?: NodeReview[];
  /** whether the draft, as it stands, passes the gate of an accepted review (derived by the graph, not stored): `valid: false` blocks the Accept, `message` is the rule that failed; absent: not checked */
  validation?: ImpactValidation;
  via?: string;
  recheck?: boolean;
  producedBy?: string;
  /** flow branch that declared it (a candidate until the flow is adopted) */
  flow?: string;
  /** replaced by an adopted flow */
  superseded?: boolean;
  createdAt?: string;
  /** the change impacts this one is derived from */
  derivedFrom?: string[];
  /** the execution (journal record of an action run) that wrote it */
  execution?: string;
  /** impact / proposal items it is derived from (empty when written directly) */
  items?: string[];
}

/** process.started | tick | action | approval | process.ended */
export type ExecutionKind = 'process.started' | 'tick' | 'action' | 'approval' | 'process.ended';

export interface ModelCall {
  provider?: string;
  model?: string;
  inputTokens?: Int64;
  outputTokens?: Int64;
  durationMs?: Int64;
  error?: string;
}

export interface ToolUse {
  name?: string;
  durationMs?: Int64;
  error?: string;
}

/**
 * Entry in a change's execution journal (ADR 0011): tick (observation +
 * planning), action execution, human decision, process start / end.
 */
export interface ExecutionRecord {
  id?: string;
  changeId?: string;
  processId?: string;
  parentProcessId?: string;
  seq?: number;
  kind?: ExecutionKind | string;
  methodology?: string;
  methodologyVersion?: string;
  agent?: string;
  planner?: string;
  goal?: string;
  status?: string;
  step?: number;
  action?: string;
  actionKind?: string;
  /** specialization executed in place of the planned action */
  specialization?: string;
  plan?: string[];
  before?: Record<string, boolean>;
  after?: Record<string, boolean>;
  /** absent as long as the action is not finished (or errored, or waiting) */
  effectsMet?: boolean;
  items?: string[];
  /** node versions the step read (referenced on the blackboard when it started) */
  reads?: NodeRef[];
  /** item count of the change (blackboard state) when the step started / ended */
  boardBefore?: number;
  boardAfter?: number;
  /** last item of the flow before the step: where a relaunch of the step forks */
  boardLast?: string;
  inputTokens?: Int64;
  outputTokens?: Int64;
  modelCalls?: ModelCall[];
  toolCalls?: ToolUse[];
  actor?: string;
  output?: string;
  error?: string;
  traceId?: string;
  spanId?: string;
  /** tick: replanned, candidates, unknown · action: waiting, child, children · end: steps, llmCalls… */
  data?: Struct;
  startedAt?: string;
  endedAt?: string;
  durationMs?: Int64;
  /** flow branch the process runs on ('' = the main flow) */
  flow?: string;
}

export interface SharedNode {
  node?: NodeRef;
  key?: string;
  changes?: string[];
}

/** What the engine records on the flow it opens for a relaunched step (the graph keeps it opaque). */
export interface FlowOrigin {
  step?: number;
  execution?: string;
  process?: string;
  reason?: string;
}

/** Event of the action flow, carried by change items of type "flow". */
export interface FlowEvent {
  op?: 'open' | 'adopt' | 'discard' | string;
  flow?: string;
  parent?: string;
  forkAfter?: string;
  stale?: string[];
  /** opaque producer ids the flow invalidates */
  staleRuns?: string[];
  /** opaque record of the opener (a relaunch: step, execution, process, reason) */
  origin?: FlowOrigin;
  by?: string;
  /** open: the flow is an option of the change; evaluate: the evaluation */
  option?: OptionSpec;
  comment?: string;
}

/** The hypothesis of a flow opened as an option (ADR 0009 §3). */
export interface OptionSpec {
  name?: string;
  hypothesis?: string;
}

/** The values of the confidence / rounds / deadline policy (pkg/decision): the graph keeps them opaque. */
export interface DecisionPolicy {
  decider?: 'agent' | 'human' | string;
  threshold?: number;
  maxRounds?: number;
  rounds?: number;
  /** RFC 3339 */
  deadline?: string;
}

/** A graph snapshot as exported (ADR 0104): the document is JSON, format goap.graph.snapshot/v1. */
export interface GraphExport {
  document?: string;
  filename?: string;
  mediaType?: string;
  nodes?: number;
}

export interface GraphImport {
  document: string;
  title?: string;
  intent?: string;
  methodology?: string;
  ownerOrg?: string;
  projectId?: string;
}

/** The outcome of an import: the change it created (none when the graph already holds the snapshot). */
export interface GraphImportResult {
  change?: Change;
  created?: number;
  updated?: number;
  unchanged?: number;
  warnings?: string[];
}

/** One edit of the settings dialog (ADR 0122): the creation or new properties of a node, or its retirement. */
export interface SettingEdit {
  namespace: string;
  type: string;
  key: string;
  props?: Struct;
  retire?: boolean;
}

/** A node the user touched in the settings dialog, not saved yet (ADR 0037, 0122). */
export interface StagedSetting {
  key: string;
  type: string;
  /** the properties of its draft */
  props?: Struct;
  retire?: boolean;
  /** the node does not exist on main yet */
  created?: boolean;
  changeImpactId?: string;
  /** a draft was written for it */
  written?: boolean;
}

/** The pending edits of the user, one personal change per namespace. */
export interface PendingSettings {
  changes?: { namespace: string; changeId: string; nodes?: StagedSetting[] }[];
}

// --- change-stack (ADR 0133) -------------------------------------------------

/** The changes a node version or a baseline is the application of, oldest first, with the lineage they are read from. */
export type ChangeStack = ChangeStackJson;
/** A change of a stack (its title is empty when the caller may not read it). */
export type StackChange = StackChangeJson;
/** A version of the lineage of a node version. */
export type VersionStep = VersionStepJson;
/** A baseline of the lineage of a baseline. */
export type BaselineStep = BaselineStepJson;

// --- statistics of the graph --------------------------------------------------

/** What one namespace holds (the overview of the graph statistics). */
export type NamespaceSummary = NamespaceSummaryJson;
/** The statistics of a namespace: the store's rows and the shape of the state at the head of a branch. */
export type GraphStats = GraphStatsJson;
/** Min, max, mean, median, 90th percentile and sum of a list of counts. */
export type Distribution = DistributionJson;
export type NodeTypeStats = NodeTypeStatsJson;
export type LinkTypeStats = LinkTypeStatsJson;
