// Graph types (proto3 JSON).

import type { Int64, Struct } from './common';

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
  /** id of the organisational unit owning the version, and of the project the node was created in (ADR 0054) */
  owner?: string;
  project?: string;
  /** a working version, edited in place by its change until it is checked in (ADR 0076) */
  checkedOut?: boolean;
}

export interface Link {
  id?: string;
  type?: string;
  from?: NodeRef;
  to?: NodeRef;
  props?: Struct;
  changeId?: string;
}

/** An entry of the log of a change (ADR 0030): a fact, a journal record or an impact event. */
export interface LogEntry {
  /** position in the log (int64: a string in JSON) */
  seq?: Int64;
  id?: string;
  changeId?: string;
  /** <stream>.<kind>: fact.artifact, journal.schedule, impact.written… */
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
}

/** The request and the answer of one LLM call (payload of a model.call log entry). */
export interface ModelExchange {
  step?: number;
  call?: number;
  system?: string;
  messages?: { role?: string; content?: string }[];
  response?: string;
  truncated?: boolean;
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

/** An operation on the change impacts of a change (ADR 0029). */
export interface ImpactEvent {
  id?: string;
  changeId?: string;
  seq?: number;
  /** empty for a change-level event (adopted) */
  impactId?: string;
  op?: 'declared' | 'written' | 'reviewed' | 'discarded' | 'adopted' | 'landed' | 'rebased' | string;
  /** the caller: flow branch ('' = main flow), journal record of the action run, principal or component */
  flow?: string;
  execution?: string;
  by?: string;
  at?: string;
  state?: ChangeImpact;
  post?: NodeRef;
  pre?: NodeRef;
  landed?: NodeRef;
  review?: NodeReview;
  stale?: string[];
}

export interface BaselineNodesQuery {
  baselineId: string;
  /** qualified node type; empty: every type */
  type?: string;
  /** matched against the key, the type and the string properties */
  query?: string;
  offset?: number;
  /** page size (max 500) */
  limit?: number;
  includeDeleted?: boolean;
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
}

/** A name given to the state an applied change leaves (ADR 0056); not unique. */
export interface Tag {
  id?: string;
  name?: string;
  namespace?: string;
  changeId?: string;
  /** the materialised snapshot of the state, empty while it is only computed */
  baselineId?: string;
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
}

export interface Decision {
  item?: string;
  accept?: boolean;
  comment?: string;
}

export type ItemKind = 'decision' | 'artifact' | 'merge' | 'flow';

/** Status of a superseded item (rebase, merge): see its replacement's `supersedes`. */
export const ITEM_SUPERSEDED = 'superseded';

export interface ChangeItem {
  id?: string;
  kind?: ItemKind | string;
  type?: string;
  status?: string;
  /** flow branch that produced the item (empty: the main flow); flowEvent on kind 'flow' */
  flow?: string;
  flowEvent?: FlowEvent;
  /** event of a decision point on kind 'decision_point' (ADR 0009 §4) */
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

export interface Change {
  id?: string;
  namespace?: string;
  /** branch the change works on (change-<id> when it has its own) */
  branch?: string;
  /** sub-change: parent change and responsible OrgUnit key */
  parentId?: string;
  ownerOrg?: string;
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
  owner?: string;
}

export interface ChangeImpact {
  id?: string;
  key?: string;
  type?: string;
  intent?: 'created' | 'modified' | string;
  rationale?: string;
  /** released version the change starts from (none for a created node) */
  pre?: NodeRef;
  /** version written on the change branch (none while only planned) */
  post?: NodeRef;
  /** version on the target branch once applied */
  landed?: NodeRef;
  review?: 'proposed' | 'accepted' | 'rejected' | string;
  reviews?: NodeReview[];
  via?: string;
  recheck?: boolean;
  producedBy?: string;
  /** flow branch that declared it (a candidate until the flow is adopted) */
  flow?: string;
  /** replaced by an adopted flow */
  superseded?: boolean;
  createdAt?: string;
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

/** Event of the action flow, carried by change items of kind "flow". */
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
