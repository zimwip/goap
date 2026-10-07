// Engine types (proto3 JSON).

import type { Int64, JsonValue, Struct } from './common';
import type { ChangeImpact, DecisionPolicy, FlowOrigin, GraphNode, ItemKind, NodeRef, OptionSpec } from './graph';
import type { DocumentReference, PlannerKind, Responsibilities, TriggerType } from './registry';

// --- engine -----------------------------------------------------------------

export type ProcessStatus = 'clarifying' | 'running' | 'waiting' | 'completed' | 'stuck' | 'failed' | 'superseded';

export interface Turn {
  role?: string;
  text?: string;
}

export interface Candidate {
  goal?: string;
  confidence?: number;
  reason?: string;
  agent?: string;
  methodology?: string;
}


export interface Usage {
  inputTokens?: Int64;
  outputTokens?: Int64;
  llmCalls?: number;
  toolCalls?: number;
}

export interface LlmCall {
  provider?: string;
  model?: string;
  inputTokens?: Int64;
  outputTokens?: Int64;
  durationMs?: Int64;
  error?: string;
}

export interface ToolCall {
  name?: string;
  durationMs?: Int64;
  error?: string;
}

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

export interface LogLine {
  time?: string;
  level?: LogLevel | string;
  message?: string;
  processId?: string;
  action?: string;
  step?: number;
}

export interface Principal {
  subject?: string;
  org?: string;
  /** the caller's active project (ADR 0039), from their token */
  project?: string;
  roles?: string[];
}
/** One structure of the organisation (ADR 0054): the hierarchy of the units or of the projects. */
export interface Structure {
  kind: string;
  /** qualified node type of the hierarchy */
  type: string;
  namespace: string;
  /** qualified link type from a child to its parent */
  parent: string;
  /** key of the root node */
  root: string;
  selfParent?: boolean;
  /** property flagging the default member of the hierarchy ('': the root is) */
  default?: string;
  /** node types belonging to the structure (the tagged type and its subtypes) */
  types?: string[];
}

/** A built-in platform role (an Assignment naming no project grants it platform-wide). */
export interface PlatformRole {
  name: string;
  description?: string;
}

/**
 * The caller as the platform sees it, with what is derived from them (GET /api/whoami, ADR 0070): the principal's
 * fields, the structures and names the web builds nodes with, the platform roles, what the caller may attempt (hints:
 * the server enforces every call).
 */
export interface Session extends Principal {
  can: { administer: boolean; approve: boolean; lowerCriticality: boolean };
  structures: Structure[];
  names: {
    namespaces: { organisation: string; platform: string; meta: string };
    types: { orgUnit: string; projectUnit: string; user: string; assignment: string; adapter: string; policy: string; mcp: string };
    links: { partOf: string; projectPartOf: string; memberOf: string; assignsOrg: string; assignsProject: string };
    keys: { user: string; assignment: string; platformScope: string; policy: string };
    roles: { admin: string };
    props: { waiting: string };
  };
  platformRoles: PlatformRole[];
}

/** Inconsistency found in the content of a blackboard. */
export interface BoardIssue {
  /** item where the problem shows */
  item?: string;
  /** item to blame: relaunch the step that produced it */
  culprit?: string;
  /** structure | dangling | derived_from_invalid | reference | outdated | rule | impact | duplicate */
  code?: string;
  message?: string;
  severity?: 'error' | 'warning' | string;
}

/** The earliest step to restart from to fix an inconsistent blackboard. */
export interface RelaunchProposal {
  process?: string;
  step?: number;
  action?: string;
  reason?: string;
  culprits?: string[];
}

export interface HumanTask {
  /** input: enter items · approval: approve or reject · agent: waiting on a sub-agent · flow: adopt or discard a relaunched flow · board: inconsistent blackboard · relaunched: waiting for a relaunched flow · condition: waiting for conditions established outside the process */
  kind?: 'input' | 'approval' | 'agent' | 'flow' | 'board' | 'relaunched' | 'condition' | 'unblock' | string;
  /** kind "condition": the conditions awaited; "unblock" (stuck): those that would unblock it ("name" expected true, "!name" expected false) */
  conditions?: string[];
  /** kind "board": what is wrong, and the step to restart from (absent when none can be) */
  issues?: BoardIssue[];
  proposal?: RelaunchProposal;
  /** kind "relaunched": the flow whose decision the process waits for */
  flowId?: string;
  /** kind "agent": process of the awaited sub-agent */
  childProcessId?: string;
  /** permission required to approve (e.g. change:apply) */
  permission?: string;
  action?: string;
  description?: string;
  instructions?: string;
  /** kind "input": qualified node types (<namespace>@<NodeType>) the task may create or pick to edit (empty: every type of the change's namespace) */
  nodeTypes?: string[];
  step?: number;
  /** the step of a process the task belongs to: what to do and how (ADR 0034, ADR 0035 §2) */
  context?: StepContext;
}

/** The step of a process a task or an action carries out. */
export interface StepContext {
  process?: string;
  path?: string;
  name?: string;
  description?: string;
  guidance?: string;
  checklist?: string[];
  deliverables?: string[];
  references?: DocumentReference[];
  /** the method chosen to carry the step out */
  method?: string;
  /** the roles of the step */
  roles?: Responsibilities;
}

/** Where a run stands in the steps of the process its agent runs (ADR 0035 §3). */
/** One part of a condition formula with the value it took (ExplainCondition). */
export interface ConditionTerm {
  text?: string;
  value?: string;
  skipped?: boolean;
  error?: string;
  terms?: ConditionTerm[];
}

export interface ConditionExplanation {
  condition?: string;
  expr?: string;
  value?: boolean;
  error?: string;
  /** declared established by a person whatever the formula says */
  waived?: boolean;
  root?: ConditionTerm;
  /** blackboard variables the formula reads, as JSON text */
  inputs?: Record<string, string>;
  note?: string;
}

export interface ProcessProgress {
  processId?: string;
  methodology?: string;
  process?: string;
  description?: string;
  status?: string;
  error?: string;
  steps?: StepProgress[];
  /** steps that run something: done or skipped, out of total */
  done?: number;
  total?: number;
}

export type StepState = 'done' | 'skipped' | 'active' | 'waiting' | 'ready' | 'todo' | 'blocked';

/** A stream of a step with foreach (ADR 0050): the element or group, the method chosen for it, its agent instance. */
export interface Lane {
  item?: string;
  method?: string;
  processId?: string;
  status?: string;
}

export interface StepProgress {
  path?: string;
  lanes?: Lane[];
  name?: string;
  description?: string;
  method?: string;
  target?: string;
  state?: StepState | string;
  /** entry conditions that do not hold ("!name": expected false) */
  missing?: string[];
  runs?: number;
  childProcessIds?: string[];
  /** waiting: the kind of the task (input, approval) and the permission an approval needs */
  waiting?: string;
  permission?: string;
  guidance?: string;
  checklist?: string[];
  references?: DocumentReference[];
  steps?: StepProgress[];
  /** the method chosen for a step that names a capability */
  chosen?: string;
  /** the roles in force for the step */
  roles?: Responsibilities;
}

export interface Step {
  index?: number;
  action?: string;
  plan?: string[];
  before?: Record<string, boolean>;
  after?: Record<string, boolean>;
  items?: string[];
  effectsMet?: boolean;
  approvedBy?: string;
  output?: string;
  error?: string;
  startedAt?: string;
  endedAt?: string;
  usage?: Usage;
  llmCalls?: LlmCall[];
  toolCalls?: ToolCall[];
  logs?: LogLine[];
  /** processes of sub-agents started by the step */
  childProcessIds?: string[];
  /** sandbox that executed the step (script actions) */
  sandbox?: string;
}

export interface Process {
  id?: string;
  methodology?: string;
  changeId?: string;
  status?: ProcessStatus | string;
  goal?: string;
  turns?: Turn[];
  question?: string;
  candidates?: Candidate[];
  pending?: HumanTask;
  plan?: string[];
  world?: Record<string, boolean>;
  unknown?: Record<string, string>;
  steps?: Step[];
  disabled?: string[];
  error?: string;
  createdAt?: string;
  updatedAt?: string;
  initiator?: Principal;
  agent?: string;
  planner?: PlannerKind | string;
  /** calling process (sub-agent) */
  parentId?: string;
  usage?: Usage;
  baselineId?: string;
  title?: string;
  /** OpenTelemetry trace (root "process" span) */
  traceId?: string;
  /** "<agent>/<trigger>" when started by a trigger */
  trigger?: string;
  /** process whose event fired the trigger */
  cause?: string;
  /** flow branch the process works on (relaunched step); empty: the main flow */
  flow?: string;
  /** process replaced when the flow is adopted, and the step that was restarted */
  relaunchOf?: string;
  fromStep?: number;
}




/** Flow branch of a change: a relaunched step, adopted or discarded by a human. */
export interface Flow {
  id?: string;
  parent?: string;
  forkAfter?: string;
  /** opaque record of the opener; a relaunch writes the restarted step, its run, the process (replaced if the flow is adopted) and why */
  origin?: FlowOrigin;
  staleRuns?: string[];
  status?: 'open' | 'adopted' | 'discarded' | string;
  /** items the relaunched step invalidated (stale while open, superseded once adopted) */
  stale?: string[];
  openedAt?: string;
  decidedAt?: string;
  decidedBy?: string;
  /** adopted flows that replace the same items: an open flow that competes cannot be adopted */
  competesWith?: string[];
  /** set on an option (ADR 0032 §6): its hypothesis, status, last evaluation, and whether the change works on it */
  option?: OptionSpec;
  optionStatus?: 'exploring' | 'evaluated' | 'selected' | 'rejected' | string;
  evaluation?: string;
  active?: boolean;
}

/** A question raised by an undecidable ruling: it blocks its decision point until answered (ADR 0009 §4). */
export interface Question {
  id?: string;
  point?: string;
  text?: string;
  status?: 'open' | 'answered' | string;
  answer?: string;
  answeredBy?: string;
  /** the process that investigated it */
  process?: string;
  askedAt?: string;
}

export interface Ruling {
  outcome?: 'decided' | 'undecidable' | string;
  option?: string;
  confidence?: number;
  justification?: string;
  by?: string;
  human?: boolean;
  at?: string;
}


/** A decision point of a change: a question to settle, usually which option (ADR 0009 §4). */
export interface DecisionPoint {
  id?: string;
  question?: string;
  options?: string[];
  criteria?: string[];
  /** the policy values of the point, kept by the graph's decision policy (ADR 0067) */
  policy?: DecisionPolicy;
  openedAt?: string;
  openedBy?: string;
  status?: 'open' | 'blocked' | 'ratifying' | 'escalated' | 'decided' | string;
  questions?: Question[];
  ruling?: Ruling;
  /** only a person may rule it now: by design, or escalated */
  humanOnly?: boolean;
  /** why the policy reserved it to a person */
  escalation?: string;
  option?: string;
  decidedAt?: string;
  decidedBy?: string;
}

/** One difference between the left and the right shape of an impact's node (ADR 0083): a property, the state, the owner or a link. */
export interface FieldChange {
  /** property | state | owner | link */
  kind?: string;
  /** the property, or the link type */
  name?: string;
  /** added | removed | changed, from the left to the right */
  op?: string;
  /** the key of the target of a link */
  target?: string;
  old?: JsonValue;
  new?: JsonValue;
}

/** What one flow sees of an impact. */
export interface ImpactSide {
  impact?: string;
  /** the flow that declared the impact ('' = the main flow) */
  flow?: string;
  intent?: string;
  review?: string;
  drafted?: boolean;
  state?: string;
  owner?: string;
}

/** An impact that differs between two flows: added (right only), removed (left only) or modified. */
export interface ImpactDiff {
  node?: string;
  key?: string;
  type?: string;
  /** added | removed | modified */
  category?: string;
  left?: ImpactSide;
  right?: ImpactSide;
  changes?: FieldChange[];
}

/** The impacts two flows of a change see, compared (ADR 0083). */
export interface FlowDiff {
  left?: string;
  right?: string;
  level?: string;
  impacts?: ImpactDiff[];
  /** impacts both flows see with the same content, left out of `impacts` */
  identical?: number;
}

/** A node changed on the source branch of a merge. */
export interface MergeCandidate {
  node?: string;
  key?: string;
  type?: string;
  /** added | fast_forward | merge */
  kind?: string;
  ancestor?: NodeRef;
  ours?: NodeRef;
  theirs?: NodeRef;
  deleted?: boolean;
  /** properties at the common ancestor, on the target (ours), on the merged branch (theirs), and merged */
  base?: Struct;
  oursProps?: Struct;
  theirsProps?: Struct;
  merged?: Struct;
  /** the properties changed on both sides with different values */
  conflicts?: string[];
}

/** How a merge settles a node: its properties (a merge version is written), or skip (the target stays as is). */
export interface Resolution {
  props?: Struct;
  skip?: boolean;
}

/** A node whose version differs between two baselines. */
export interface BaselineDiff {
  node?: string;
  key?: string;
  type?: string;
  /** added | removed | changed */
  kind?: string;
  from?: GraphNode;
  to?: GraphNode;
}

/** What the web writes of a node in a change (lifecycle.writeNodeInChange): properties and links edited on its working
 * version, then a lifecycle state (a transition of its own, ADR 0076). */
export interface ImpactWrite {
  props?: Struct;
  state?: string;
  addLinks?: LinkWrite[];
  removeLinks?: string[];
}

/** A link written with a node version: its type and the node version it points to. */
export interface LinkWrite {
  type: string;
  to: NodeRef;
  props?: Struct;
}

/** A node a merge or a split works on: a change impact, a node id or a key (ADR 0077). */
export interface NodeName {
  changeImpactId?: string;
  nodeId?: string;
  key?: string;
}

/** A node a merge or a split creates. */
export interface NodeCreateSpec {
  key: string;
  type: string;
  props?: Struct;
  owner?: string;
  rationale?: string;
  links?: LinkWrite[];
}

/** A link of another node to a split source that the split leaves as it is (it becomes suspect, ADR 0003). */
export interface SuspectLink {
  from?: NodeRef;
  fromKey?: string;
  type?: string;
  to?: NodeRef;
  toKey?: string;
}

/** What a merge or a split did to the change (ADR 0077). */
export interface Restructured {
  /** the new nodes (created, checked out, with their origins) */
  successors?: ChangeImpact[];
  /** the merged or split nodes, `via` the impact of their parent */
  sources?: ChangeImpact[];
  /** the nodes whose links moved, each checked out */
  parents?: ChangeImpact[];
  suspect?: SuspectLink[];
}

export interface MergePlan {
  from?: string;
  into?: string;
  intoHead?: string;
  candidates?: MergeCandidate[];
}

/** State of a trigger of a published agent. */
export interface TriggerState {
  methodology?: string;
  agent?: string;
  name?: string;
  description?: string;
  type?: TriggerType | string;
  event?: string;
  schedule?: string;
  enabled?: boolean;
  fires?: number;
  lastFired?: string;
  nextFire?: string;
  lastProcessId?: string;
  lastError?: string;
}

export interface ListProcessesRequest {
  /** only processes started by the caller */
  mine?: boolean;
  statuses?: string[];
  /** only root processes (no sub-agents) */
  rootsOnly?: boolean;
}

export interface AttachChangeRequest {
  processId: string;
  /** reuse an existing change, or empty to create one (same defaulting as StartProcess) */
  changeId?: string;
  title?: string;
  intent?: string;
  namespace?: string;
  ownerOrg?: string;
  baselineId?: string;
}

/** One append-only entry of a process's own log (ADR 0031): turn-by-turn state independent
 *  of any change, covering a process whether or not it ever attaches to one. */
export interface ProcessLogEntry {
  seq?: Int64;
  processId?: string;
  type?: string;
  /** the entry's payload, as JSON */
  payload?: string;
  at?: string;
}

export type EventType = 'started' | 'intent' | 'step' | 'waiting' | 'completed' | 'stuck' | 'failed' | 'log';

/** Message on the WatchEvents stream. */
export interface WatchEvent {
  type?: EventType | string;
  time?: string;
  process?: Process;
  log?: LogLine;
}

/** Item in the engine's input format (pkg/engine.ItemInput). */
/** An operation on a change impact (ADR 0024): declare, write or review, applied in order. */
export interface NodeOp {
  op: 'declare' | 'write' | 'review';
  /** local reference of a declared change impact ("#nN") */
  ref?: string;
  intent?: 'created' | 'modified';
  key?: string;
  type?: string;
  rationale?: string;
  /** write, review: a node key or a "#nN" reference */
  node?: string;
  props?: Struct;
  state?: string;
  links?: { type: string; to: string }[];
  accept?: boolean;
  comment?: string;
}

export interface ItemInput {
  ref?: string;
  kind: ItemKind | 'changeImpact';
  /** kind changeImpact: the operation */
  changeImpact?: NodeOp;
  type?: string;
  decision?: { item: string; accept: boolean; comment?: string };
  data?: Struct;
  derivedFrom?: string[];
}

export interface StartProcessRequest {
  /** empty: identify among all published methodologies and their agents */
  methodology?: string;
  /** restricts identification to this agent */
  agent?: string;
  baselineId?: string;
  changeId?: string;
  title?: string;
  intent: string;
  goal?: string;
  /** key of the OrgUnit holding the change (empty: the default organisation) */
  ownerOrg?: string;
  vars?: Struct;
}
