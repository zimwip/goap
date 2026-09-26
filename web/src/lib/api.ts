// Minimal client for the GOAP gateway (Connect protocol, JSON encoding).
// Each RPC is a `POST /{package.Service}/{Method}` with a JSON body
// (proto3 JSON: fields in lowerCamelCase, default values omitted).

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

const TOKEN_KEY = 'goap.token';

/** Base for RPC URLs: relative by default (the Vite server proxies `/goap.*`). */
export const BASE = (import.meta.env.VITE_GOAP_BASE_URL as string | undefined) ?? '';

/** Base for the Jaeger UI, used by the "Trace" links. */
export const JAEGER_URL = ((import.meta.env.VITE_GOAP_JAEGER_URL as string | undefined) ?? 'http://localhost:16686').replace(
  /\/+$/,
  '',
);

export class RpcError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = 'RpcError';
    this.code = code;
    this.status = status;
  }
}

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

const tokenListeners = new Set<() => void>();

/** Subscribe to token changes (restarts streams). Returns the unsubscribe function. */
export function onTokenChange(fn: () => void): () => void {
  tokenListeners.add(fn);
  return () => tokenListeners.delete(fn);
}

export function setToken(token: string | null): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    // storage unavailable (private browsing, etc.): ignore
  }
  for (const fn of tokenListeners) fn();
}

export async function rpc<TReq extends object, TRes>(
  service: string,
  method: string,
  body: TReq,
  signal?: AbortSignal,
): Promise<TRes> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;

  let res: Response;
  try {
    res = await fetch(`${BASE}/${service}/${method}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
      signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }

  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }

  if (!res.ok) {
    const err = (data ?? {}) as { code?: string; message?: string };
    throw new RpcError(err.code ?? 'unknown', err.message ?? (text || res.statusText), res.status);
  }
  return (data ?? {}) as TRes;
}

/** Human-readable error message for the UI. */
export function errorMessage(e: unknown): string {
  if (e instanceof RpcError) {
    if (e.code === 'permission_denied')
      return `Access denied: you do not have the rights required for this operation${e.message ? ` (${e.message})` : ''}.`;
    if (e.code === 'unauthenticated')
      return `Authentication required: configure a valid access token${e.message ? ` (${e.message})` : ''}.`;
    return e.code ? `${e.code}: ${e.message}` : e.message;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

// ---------------------------------------------------------------------------
// Types (proto3 JSON). Fields are optional because default values are
// omitted on serialization.
// ---------------------------------------------------------------------------

export type JsonValue = null | boolean | number | string | JsonValue[] | { [k: string]: JsonValue };
export type Struct = { [k: string]: JsonValue };
type Empty = Record<string, never>;

// --- registry ---------------------------------------------------------------

/** draft: editable · published: frozen (only executable one) · archived: read-only */
export type MethodologyStatus = 'draft' | 'published' | 'archived';

export interface LifecycleState {
  name?: string;
  description?: string;
  /** working state: only held through a change */
  editable?: boolean;
  final?: boolean;
}

export interface LifecycleTransition {
  name?: string;
  from?: string;
  to?: string;
  /** "type:action" the actor must hold (default node:transition) */
  permission?: string;
  /** CEL over node, children and change */
  guard?: string;
  requiresAttributes?: string[];
  requiresOutgoingLinks?: string[];
  /** documents: allowed states of the contained children */
  childrenStates?: string[];
  /** transition_guard algorithm instances, run in order after the CEL guard */
  guards?: string[];
  /** transition_action algorithm instances, run in order once the transition is accepted */
  actions?: string[];
}

export interface Lifecycle {
  /** identifies the lifecycle in its domain; node types name it */
  name?: string;
  initial?: string;
  states?: LifecycleState[];
  transitions?: LifecycleTransition[];
}

export interface NodeType {
  name?: string;
  description?: string;
  properties?: string[];
  /** parent type: the subtype inherits its properties and link types */
  extends?: string;
  /** name of the domain lifecycle of the nodes (inherited through extends) */
  lifecycle?: string;
  /** the type embeds nodes of these types through "contains" links */
  document?: { contains?: string[] };
  /** absent: change controlled */
  changeControlled?: boolean;
  /** property validator instances (ADR 0018), in call order */
  validators?: PropertyValidator[];
}

/** Plugs an algorithm instance of type property_validator on a property. */
export interface PropertyValidator {
  property?: string;
  instance?: string;
}

/** Fixed algorithm types: the extension points of the platform. */
export type AlgorithmType = 'property_validator' | 'transition_guard' | 'transition_action';
export type AlgorithmParamType = 'string' | 'number' | 'boolean' | 'regex' | 'enum' | 'strings' | 'json';

export interface AlgorithmParam {
  name?: string;
  type?: AlgorithmParamType | string;
  description?: string;
  required?: boolean;
  defaultValue?: unknown;
  /** allowed values of an enum */
  values?: string[];
}

/** Script of a fixed type with declared parameters; declared by a shared domain. */
export interface Algorithm {
  name?: string;
  description?: string;
  type?: AlgorithmType | string;
  language?: ScriptLanguage | string;
  code?: string;
  params?: AlgorithmParam[];
}

/** Parameter values of an algorithm: what gets plugged. */
export interface AlgorithmInstance {
  name?: string;
  description?: string;
  algorithm?: string;
  values?: Record<string, unknown>;
}

export interface RunAlgorithmResponse {
  ok?: boolean;
  failures?: string[];
  /** the script itself failed (does not compile, throws, times out) */
  error?: string;
  set?: Record<string, unknown>;
  unset?: string[];
  logs?: string[];
}

export interface LinkType {
  name?: string;
  from?: string;
  to?: string;
}

export interface Condition {
  name?: string;
  description?: string;
  /** CEL expression evaluated against the blackboard. */
  expr?: string;
}

export interface ProduceSpec {
  op?: 'create_node' | 'update_node' | string;
  nodeType?: string;
}

export interface LinkSpec {
  type?: string;
  direction?: 'out' | 'in' | string;
}

export interface Expectation {
  forEach?: 'impacts' | 'proposals' | 'items' | 'artifacts' | string;
  where?: string;
  produce?: ProduceSpec;
  link?: LinkSpec;
}

export type ActionKind = 'llm' | 'script' | 'tool' | 'human' | 'builtin' | 'abstract';
export type ScriptLanguage = 'javascript' | 'go';
export type PlannerKind = 'goap' | 'utility' | 'hybrid';

export interface Action {
  name?: string;
  description?: string;
  kind?: ActionKind | string;
  pre?: Record<string, boolean>;
  effects?: Record<string, boolean>;
  cost?: number;
  expects?: Expectation;
  /** "<resource>:<action>" required of the initiator, e.g. change:apply */
  permission?: string;
  model?: string;
  prompt?: string;
  tool?: string;
  builtin?: string;
  instructions?: string;
  params?: Struct;
  /** script actions: javascript | go */
  language?: ScriptLanguage | string;
  /** code executed in the sandbox with the `ctx` DSL */
  code?: string;
  /** numeric CEL expression (utility / hybrid planners) */
  utility?: string;
  /** specialization: "<action>" or "<methodology>/<action>" (not planned) */
  specializes?: string;
  /** CEL guard for the specialization, evaluated against the blackboard */
  when?: string;
  /** specialization priority (highest wins) */
  priority?: number;
  /** effects reached over several runs (a run that produces items is progress) */
  incremental?: boolean;
  /** MCPs used by an llm / script action (a tool action names `<mcp>/<tool>`) */
  mcps?: string[];
}

export type TriggerType = 'event' | 'schedule';
export const TRIGGER_EVENTS = [
  'change.created',
  'change.applied',
  'change.item_added',
  'process.completed',
  'process.failed',
  'process.stuck',
  'methodology.published',
] as const;

/** Trigger: automatic run of an agent (outside the intent loop). */
export interface Trigger {
  name?: string;
  description?: string;
  type?: TriggerType | string;
  /** "event" triggers */
  event?: string;
  /** CEL filter on the event */
  filter?: string;
  /** "schedule" triggers: 5-field cron, UTC */
  schedule?: string;
  goal?: string;
  intent?: string;
  /** new_change (default) | event_change */
  target?: 'new_change' | 'event_change' | string;
  roles?: string[];
  enabled?: boolean;
}

/** Agent (Embabel terminology): a planner and its eligible actions. */
export interface Agent {
  name?: string;
  description?: string;
  examples?: string[];
  planner?: PlannerKind | string;
  /** eligible action names (empty: all) */
  actions?: string[];
  /** goal names (empty: all) */
  goals?: string[];
  triggers?: Trigger[];
  /** MCPs whose tools the llm / script actions of the agent may use */
  mcps?: string[];
}

export interface Goal {
  name?: string;
  description?: string;
  examples?: string[];
  pre?: Record<string, boolean>;
  value?: number;
}

export interface Methodology {
  name?: string;
  version?: string;
  description?: string;
  status?: MethodologyStatus | string;
  /** shared domain "<name>[@<version>]" used instead of embedded node / link types */
  domainRef?: string;
  /** graph namespace the changes of the methodology act on (default sdlc) */
  namespace?: string;
  nodeTypes?: NodeType[];
  linkTypes?: LinkType[];
  lifecycles?: Lifecycle[];
  conditions?: Condition[];
  actions?: Action[];
  goals?: Goal[];
  agents?: Agent[];
  createdAt?: string;
  updatedAt?: string;
  publishedAt?: string;
  updatedBy?: string;
}

/** Shared object part of the model: node types and link types, versioned on its own. */
export interface Domain {
  name?: string;
  version?: string;
  description?: string;
  status?: MethodologyStatus | string;
  nodeTypes?: NodeType[];
  linkTypes?: LinkType[];
  lifecycles?: Lifecycle[];
  algorithms?: Algorithm[];
  algorithmInstances?: AlgorithmInstance[];
  createdAt?: string;
  updatedAt?: string;
  publishedAt?: string;
  updatedBy?: string;
}

export interface DomainSummary {
  name?: string;
  version?: string;
  description?: string;
  status?: string;
  nodeTypeCount?: number;
  linkTypeCount?: number;
  updatedAt?: string;
  publishedAt?: string;
}

/** Methodology version referencing a domain version. */
export interface DomainUser {
  name?: string;
  version?: string;
  status?: string;
}

export interface GoalSummary {
  name?: string;
  description?: string;
}

export interface AgentSummary {
  name?: string;
  description?: string;
  planner?: string;
}

export interface MethodologySummary {
  name?: string;
  version?: string;
  description?: string;
  status?: MethodologyStatus | string;
  goals?: GoalSummary[];
  agents?: AgentSummary[];
  updatedAt?: string;
  publishedAt?: string;
}

/** Validation issue; `path` locates the field, e.g. "conditions[2].expr". */
export interface Issue {
  path?: string;
  message?: string;
}

// --- iam --------------------------------------------------------------------

/** ABAC rule: `rule` is an expression over r.sub, r.obj and r.act. */
export interface Policy {
  rule?: string;
  /** resource type or "*" */
  resource?: string;
  /** action or "*" */
  action?: string;
  effect?: 'allow' | 'deny' | string;
}

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
}

export interface Link {
  id?: string;
  type?: string;
  from?: NodeRef;
  to?: NodeRef;
  props?: Struct;
  changeId?: string;
}

export interface Baseline {
  id?: string;
  name?: string;
  parentId?: string;
  changeId?: string;
  nodes?: Record<string, number>;
  createdAt?: string;
}

export interface Endpoint {
  node?: NodeRef;
  item?: string;
}

export interface NodeDraft {
  base?: NodeRef;
  key?: string;
  type?: string;
  props?: Struct;
  /** transition_node: target state; create_node: state the node is born in */
  state?: string;
}

export interface LinkDraft {
  linkId?: string;
  type?: string;
  from?: Endpoint;
  to?: Endpoint;
  props?: Struct;
}

export interface Proposal {
  op?: 'create_node' | 'update_node' | 'delete_node' | 'transition_node' | 'add_link' | 'remove_link' | string;
  node?: NodeDraft;
  link?: LinkDraft;
}

export interface Decision {
  item?: string;
  accept?: boolean;
  comment?: string;
}

export type ItemKind = 'impact' | 'proposal' | 'decision' | 'artifact' | 'merge' | 'flow';

/** Status of a superseded item (rebase, merge): see its replacement's `supersedes`. */
export const ITEM_SUPERSEDED = 'superseded';

export interface ChangeItem {
  id?: string;
  kind?: ItemKind | string;
  type?: string;
  status?: string;
  /** impact: the "pre" version (released, in the reference baseline) */
  target?: NodeRef;
  /** impact: the "post" side, the proposal producing the new version (item) or that version (node) */
  post?: { node?: NodeRef; item?: string };
  /** flow branch that produced the item (empty: the main flow); flowEvent on kind 'flow' */
  flow?: string;
  flowEvent?: FlowEvent;
  proposal?: Proposal;
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

export interface ChangeSet {
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
  status?: 'draft' | 'active' | 'merge_pending' | 'applied' | 'abandoned' | string;
  baselineId?: string;
  resultBaselineId?: string;
  data?: Struct;
  items?: ChangeItem[];
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
}

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

/** 64-bit integer: proto3 JSON serializes it as a string. */
export type Int64 = number | string;

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
  roles?: string[];
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
  /** input: enter items · approval: approve or reject · agent: waiting on a sub-agent · flow: adopt or discard a relaunched flow · board: inconsistent blackboard · relaunched: waiting for a relaunched flow */
  kind?: 'input' | 'approval' | 'agent' | 'flow' | 'board' | 'relaunched' | string;
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
  step?: number;
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
  /** flow branch the process works on (relaunched step); empty: the main flow */
  flow?: string;
  /** process replaced when the flow is adopted, and the step that was restarted */
  relaunchOf?: string;
  fromStep?: number;
}

/** Event of the action flow, carried by change items of kind "flow". */
export interface FlowEvent {
  op?: 'open' | 'adopt' | 'discard' | string;
  flow?: string;
  parent?: string;
  forkAfter?: string;
  fromStep?: number;
  execution?: string;
  process?: string;
  reason?: string;
  stale?: string[];
  by?: string;
}

/** Flow branch of a change: a relaunched step, adopted or discarded by a human. */
export interface Flow {
  id?: string;
  parent?: string;
  forkAfter?: string;
  fromStep?: number;
  execution?: string;
  /** the process whose step was relaunched (replaced if the flow is adopted) */
  process?: string;
  reason?: string;
  status?: 'open' | 'adopted' | 'discarded' | string;
  /** items the relaunched step invalidated (stale while open, superseded once adopted) */
  stale?: string[];
  openedAt?: string;
  decidedAt?: string;
  decidedBy?: string;
  /** graph (domain) branch the flow's proposals were last applied on, for review */
  branch?: string;
  branches?: string[];
  materialized?: string[];
  /** proposals already merged into the change branch (adopted flow) */
  merged?: string[];
  /** adopted flows that replace the same items: an open flow that competes cannot be adopted */
  competesWith?: string[];
}

/** A node changed on the source branch of a merge. */
export interface MergeCandidate {
  node?: string;
  key?: string;
  type?: string;
  /** added | fast_forward | merge */
  kind?: string;
  deleted?: boolean;
  conflicts?: string[];
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

export type EventType = 'started' | 'intent' | 'step' | 'waiting' | 'completed' | 'stuck' | 'failed' | 'log';

/** Message on the WatchEvents stream. */
export interface WatchEvent {
  type?: EventType | string;
  time?: string;
  process?: Process;
  log?: LogLine;
}

/** Item in the engine's input format (pkg/engine.ItemInput). */
export interface ItemInput {
  ref?: string;
  kind: ItemKind;
  type?: string;
  /** Key of the targeted node. */
  target?: string;
  proposal?: Struct;
  decision?: { item: string; accept: boolean; comment?: string };
  data?: Struct;
  derivedFrom?: string[];
}

// ---------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------

const REGISTRY = 'goap.registry.v1.RegistryService';
const GRAPH = 'goap.graph.v1.GraphService';
const ENGINE = 'goap.engine.v1.EngineService';

const IAM = 'goap.iam.v1.IamService';
const MODEL = 'goap.model.v1.ModelService';

export const ENGINE_SERVICE = ENGINE;

type NameVersion = { name: string; version: string };

export const registry = {
  /** `allVersions`: all versions (drafts, archived) instead of the latest by name. */
  listMethodologies: (allVersions = false, signal?: AbortSignal) =>
    rpc<{ allVersions?: boolean }, { methodologies?: MethodologySummary[] }>(
      REGISTRY,
      'ListMethodologies',
      allVersions ? { allVersions } : {},
      signal,
    ),
  /** empty `version`: latest published version. */
  getMethodology: (name: string, version = '', signal?: AbortSignal) =>
    rpc<NameVersion, { methodology?: Methodology }>(REGISTRY, 'GetMethodology', { name, version }, signal),
  saveMethodology: (methodology: Methodology) =>
    rpc<{ methodology: Methodology }, { methodology?: Methodology; issues?: Issue[] }>(REGISTRY, 'SaveMethodology', {
      methodology,
    }),
  validateMethodology: (methodology: Methodology) =>
    rpc<{ methodology: Methodology }, { issues?: Issue[] }>(REGISTRY, 'ValidateMethodology', { methodology }),
  publishMethodology: (name: string, version: string) =>
    rpc<NameVersion, { methodology?: Methodology }>(REGISTRY, 'PublishMethodology', { name, version }),
  createVersion: (name: string, fromVersion: string, newVersion: string) =>
    rpc<{ name: string; fromVersion: string; newVersion: string }, { methodology?: Methodology }>(
      REGISTRY,
      'CreateVersion',
      { name, fromVersion, newVersion },
    ),
  /** Deletes a draft, or archives a published version. */
  deleteMethodology: (name: string, version: string) =>
    rpc<NameVersion, Empty>(REGISTRY, 'DeleteMethodology', { name, version }),
  importMethodology: (yaml: string, publish: boolean) =>
    rpc<{ yaml: string; publish?: boolean }, { methodology?: Methodology; issues?: Issue[] }>(
      REGISTRY,
      'ImportMethodology',
      publish ? { yaml, publish } : { yaml },
    ),
  exportMethodology: (name: string, version: string) =>
    rpc<NameVersion, { yaml?: string; filename?: string }>(REGISTRY, 'ExportMethodology', { name, version }),

  // --- shared domains (no change / impact / proposal involved) ---
  listDomains: (allVersions = false, signal?: AbortSignal) =>
    rpc<{ allVersions?: boolean }, { domains?: DomainSummary[] }>(
      REGISTRY,
      'ListDomains',
      allVersions ? { allVersions } : {},
      signal,
    ),
  /** empty `version`: latest published version. */
  getDomain: (name: string, version = '', signal?: AbortSignal) =>
    rpc<NameVersion, { domain?: Domain }>(REGISTRY, 'GetDomain', { name, version }, signal),
  saveDomain: (domain: Domain) =>
    rpc<{ domain: Domain }, { domain?: Domain; issues?: Issue[] }>(REGISTRY, 'SaveDomain', { domain }),
  validateDomain: (domain: Domain) => rpc<{ domain: Domain }, { issues?: Issue[] }>(REGISTRY, 'ValidateDomain', { domain }),
  publishDomain: (name: string, version: string) =>
    rpc<NameVersion, { domain?: Domain }>(REGISTRY, 'PublishDomain', { name, version }),
  createDomainVersion: (name: string, fromVersion: string, newVersion: string) =>
    rpc<{ name: string; fromVersion: string; newVersion: string }, { domain?: Domain }>(REGISTRY, 'CreateDomainVersion', {
      name,
      fromVersion,
      newVersion,
    }),
  /** Deletes a draft, or archives a published version. */
  deleteDomain: (name: string, version: string) => rpc<NameVersion, Empty>(REGISTRY, 'DeleteDomain', { name, version }),
  importDomain: (yaml: string, publish: boolean) =>
    rpc<{ yaml: string; publish?: boolean }, { domain?: Domain; issues?: Issue[] }>(
      REGISTRY,
      'ImportDomain',
      publish ? { yaml, publish } : { yaml },
    ),
  exportDomain: (name: string, version: string) =>
    rpc<NameVersion, { yaml?: string; filename?: string }>(REGISTRY, 'ExportDomain', { name, version }),
  /** Methodology versions referencing a domain version (unpinned references included). */
  getDomainUsage: (name: string, version: string, signal?: AbortSignal) =>
    rpc<NameVersion, { methodologies?: DomainUser[] }>(REGISTRY, 'GetDomainUsage', { name, version }, signal),
  /** Tries an algorithm on a sample input; nothing is saved. */
  runAlgorithm: (algorithm: Algorithm, values: Record<string, unknown>, input: Record<string, unknown>) =>
    rpc<{ algorithm: Algorithm; values: Record<string, unknown>; input: Record<string, unknown> }, RunAlgorithmResponse>(
      REGISTRY,
      'RunAlgorithm',
      { algorithm, values, input },
    ),
};

export const iam = {
  whoAmI: (signal?: AbortSignal) => rpc<Empty, { principal?: Principal }>(IAM, 'WhoAmI', {}, signal),
  listPolicies: (signal?: AbortSignal) =>
    rpc<Empty, { policies?: Policy[] }>(IAM, 'ListPolicies', {}, signal),
  addPolicy: (policy: Policy) => rpc<{ policy: Policy }, { policy?: Policy }>(IAM, 'AddPolicy', { policy }),
  removePolicy: (policy: Policy) => rpc<{ policy: Policy }, Empty>(IAM, 'RemovePolicy', { policy }),
};

export interface ImpactView {
  item?: string;
  key?: string;
  type?: string;
  pre?: NodeRef;
  preState?: string;
  post?: NodeRef;
  postState?: string;
}

export interface SharedNode {
  node?: NodeRef;
  key?: string;
  changes?: string[];
}

export const graph = {
  listBaselines: (signal?: AbortSignal) =>
    rpc<Empty, { baselines?: Baseline[] }>(GRAPH, 'ListBaselines', {}, signal),
  getBaselineGraph: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { baseline?: Baseline; nodes?: GraphNode[]; links?: Link[]; suspectLinks?: Link[] }>(
      GRAPH,
      'GetBaselineGraph',
      { id },
      signal,
    ),
  listChanges: (signal?: AbortSignal) =>
    rpc<Empty, { changes?: ChangeSet[] }>(GRAPH, 'ListChanges', {}, signal),
  getChange: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { change?: ChangeSet }>(GRAPH, 'GetChange', { id }, signal),
  getBranch: (name: string, signal?: AbortSignal) =>
    rpc<{ name: string }, { branch?: { name?: string; head?: string }; head?: Baseline }>(GRAPH, 'GetBranch', { name }, signal),
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
    ownerOrg?: string;
  }) => rpc<typeof req, { change?: ChangeSet }>(GRAPH, 'CreateChange', req),
  /** Splits a change into one sub-change per organisational unit owning impacted nodes. */
  splitChange: (changeId: string) =>
    rpc<{ changeId: string }, { changes?: ChangeSet[] }>(GRAPH, 'SplitChange', { changeId }),
  listSubChanges: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { changes?: ChangeSet[] }>(GRAPH, 'ListSubChanges', { changeId }, signal),
  /** Completes a merge_pending change; resolutions are by node id. */
  mergeChange: (changeId: string, resolutions: Record<string, { props?: Struct; skip?: boolean }> = {}) =>
    rpc<
      { changeId: string; resolutions: Record<string, { props?: Struct; skip?: boolean }> },
      { change?: ChangeSet }
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
  /** Applies the proposals of a flow on a graph branch of its own (a preview). */
  materializeFlow: (changeId: string, flow: string) =>
    rpc<{ changeId: string; flow: string }, { flow?: Flow }>(GRAPH, 'MaterializeFlow', { changeId, flow }),
  /** What merging a branch into another would do. */
  planMerge: (from: string, into: string, signal?: AbortSignal) =>
    rpc<{ from: string; into: string }, { plan?: MergePlan }>(GRAPH, 'PlanMerge', { from, into }, signal),
  /** Pre/post view of the impacts of a change. */
  getImpacts: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { impacts?: ImpactView[] }>(GRAPH, 'GetImpacts', { changeId }, signal),
  /** Nodes the change shares with other open changes. */
  getSharedNodes: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { nodes?: SharedNode[] }>(GRAPH, 'GetSharedNodes', { changeId }, signal),
  /** Every version of a node, all branches. */
  listNodeVersions: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { versions?: GraphNode[] }>(GRAPH, 'ListNodeVersions', { id }, signal),
  /** Nodes the change is attached to (the versions it starts from). */
  getChangeNodes: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { nodes?: NodeRef[] }>(GRAPH, 'GetChangeNodes', { changeId }, signal),
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
  /** Creates a data node typed by a NodeType of the methodology (published, synced on the graph). */
  createObject: (methodology: string, nodeType: string, key: string, props: Struct) =>
    rpc<
      { methodology: string; nodeType: string; key: string; props: Struct },
      { node?: GraphNode; baseline?: Baseline }
    >(GRAPH, 'CreateObject', { methodology, nodeType, key, props }),
  applyChange: (changeId: string, baselineName: string) =>
    rpc<{ changeId: string; baselineName: string }, { baseline?: Baseline }>(GRAPH, 'ApplyChange', {
      changeId,
      baselineName,
    }),
};

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

export const engine = {
  startProcess: (req: StartProcessRequest) =>
    rpc<StartProcessRequest, { process?: Process }>(ENGINE, 'StartProcess', req),
  answerIntent: (processId: string, answer: string) =>
    rpc<{ processId: string; answer: string }, { process?: Process }>(ENGINE, 'AnswerIntent', {
      processId,
      answer,
    }),
  submitHumanInput: (processId: string, items: ItemInput[]) =>
    rpc<{ processId: string; items: ItemInput[] }, { process?: Process }>(ENGINE, 'SubmitHumanInput', {
      processId,
      items,
    }),
  approveAction: (processId: string, approve: boolean, comment: string) =>
    rpc<{ processId: string; approve: boolean; comment: string }, { process?: Process }>(ENGINE, 'ApproveAction', {
      processId,
      approve,
      comment,
    }),
  /** Restarts a run from one of its steps on a new flow branch; returns the new process. */
  relaunchStep: (processId: string, step: number, reason: string, guidance = '') =>
    rpc<{ processId: string; step: number; reason: string; guidance: string }, { process?: Process }>(ENGINE, 'RelaunchStep', {
      processId,
      step,
      reason,
      guidance,
    }),
  /** Adopts (previous outputs superseded) or discards a relaunched flow. */
  decideFlow: (processId: string, adopt: boolean, comment: string) =>
    rpc<{ processId: string; adopt: boolean; comment: string }, { process?: Process }>(ENGINE, 'DecideFlow', {
      processId,
      adopt,
      comment,
    }),
  /** Answers a blackboard inconsistency: relaunch the proposed step, or ignore the issues and go on. */
  resolveBoard: (processId: string, relaunch: boolean, comment: string) =>
    rpc<{ processId: string; relaunch: boolean; comment: string }, { process?: Process; relaunched?: Process }>(
      ENGINE,
      'ResolveBoard',
      { processId, relaunch, comment },
    ),
  getProcess: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { process?: Process }>(ENGINE, 'GetProcess', { id }, signal),
  listProcesses: (req: ListProcessesRequest = {}, signal?: AbortSignal) =>
    rpc<ListProcessesRequest, { processes?: Process[] }>(ENGINE, 'ListProcesses', req, signal),
  listTriggers: (signal?: AbortSignal) =>
    rpc<Empty, { triggers?: TriggerState[] }>(ENGINE, 'ListTriggers', {}, signal),
  fireTrigger: (methodology: string, agent: string, trigger: string) =>
    rpc<{ methodology: string; agent: string; trigger: string }, { process?: Process }>(ENGINE, 'FireTrigger', {
      methodology,
      agent,
      trigger,
    }),
};

// --- platform status (gateway, outside RPC) -------------------------------

export interface ServiceStatus {
  name?: string;
  status?: 'up' | 'down' | string;
  latencyMs?: number;
  error?: string;
}

export interface PlatformStatus {
  status?: 'ok' | 'degraded' | 'down' | string;
  services?: ServiceStatus[];
  time?: string;
}

/** `GET /api/status` served by the gateway. */
export async function platformStatus(signal?: AbortSignal): Promise<PlatformStatus> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  let res: Response;
  try {
    res = await fetch(`${BASE}/api/status`, { headers, signal });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }
  const text = await res.text();
  let data: PlatformStatus | undefined;
  try {
    data = text ? (JSON.parse(text) as PlatformStatus) : undefined;
  } catch {
    data = undefined;
  }
  // 503 with a status body: platform unavailable, but a usable response.
  if (data?.status) return data;
  throw new RpcError(res.status === 404 ? 'unimplemented' : 'unknown', text || res.statusText, res.status);
}

// ---------------------------------------------------------------------------
// Display utilities
// ---------------------------------------------------------------------------

/** Human-readable title of a node (`title` property, else `name`). */
export function nodeTitle(n: GraphNode | undefined): string {
  const t = n?.props?.['title'] ?? n?.props?.['name'];
  return typeof t === 'string' ? t : '';
}

export function formatDate(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString('fr-FR', { dateStyle: 'short', timeStyle: 'medium' });
}

/** Compares two version numbers "1.2.10" segment by segment (numerically when possible). */
export function compareVersions(a: string | undefined, b: string | undefined): number {
  const pa = (a ?? '').split(/[.-]/);
  const pb = (b ?? '').split(/[.-]/);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] ?? '';
    const y = pb[i] ?? '';
    const nx = Number(x);
    const ny = Number(y);
    const c = x !== '' && y !== '' && !Number.isNaN(nx) && !Number.isNaN(ny) ? nx - ny : x.localeCompare(y);
    if (c !== 0) return c;
  }
  return 0;
}

/** Increments the last numeric segment: 1.2.3 → 1.2.4. */
export function bumpPatch(version: string | undefined): string {
  const v = version ?? '';
  const m = /^(.*?)(\d+)(\D*)$/.exec(v);
  if (!m) return v ? `${v}.1` : '0.1.0';
  return `${m[1]}${Number(m[2]) + 1}${m[3]}`;
}

/** Numeric value of a proto3 integer (number or string for int64). */
export function int(v: Int64 | undefined | null): number {
  if (v === undefined || v === null || v === '') return 0;
  const n = typeof v === 'number' ? v : Number(v);
  return Number.isFinite(n) ? n : 0;
}

/** 12345 → "12 345" (grouped thousands). */
export function formatInt(v: Int64 | undefined | null): string {
  return int(v).toLocaleString('fr-FR');
}

export function formatDuration(ms: Int64 | undefined | null): string {
  const n = int(ms);
  if (n < 1000) return `${n} ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(1)} s`;
  return `${Math.floor(n / 60_000)} min ${Math.round((n % 60_000) / 1000)} s`;
}

export function formatTime(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function shortId(id: string | undefined): string {
  return id ? id.slice(0, 8) : '';
}

// --- model gateway administration ---------------------------------------------

export interface ProviderKind {
  id: string;
  label?: string;
  protocol?: string;
  defaultBaseUrl?: string;
  keyRequired?: boolean;
  description?: string;
}

export interface LlmProvider {
  name: string;
  kind: string;
  protocol: string;
  baseUrl?: string;
  enabled?: boolean;
  hasKey?: boolean;
  keyHint?: string;
  /** loaded in the running router */
  active?: boolean;
}

export interface DiscoveredModel {
  id: string;
  displayName?: string;
  registered?: boolean;
}

/** int64 fields travel as strings in proto3 JSON. */
export interface CatalogModel {
  provider: string;
  model: string;
  displayName?: string;
  enabled?: boolean;
  quotaTokens?: string | number;
  quotaPeriod?: string;
  roles?: string[];
  usedTokens?: string | number;
}

export interface ModelAlias {
  alias: string;
  provider: string;
  model: string;
}

export interface AvailableModel {
  provider: string;
  model: string;
  displayName?: string;
}

export const models = {
  /** Models (and aliases) the caller may use. */
  listAvailable: (signal?: AbortSignal) =>
    rpc<Empty, { models?: AvailableModel[]; aliases?: ModelAlias[] }>(MODEL, 'ListModels', {}, signal),
  listProviderKinds: (signal?: AbortSignal) =>
    rpc<Empty, { kinds?: ProviderKind[]; protocols?: { id: string; label?: string }[] }>(MODEL, 'ListProviderKinds', {}, signal),
  listProviders: (signal?: AbortSignal) => rpc<Empty, { providers?: LlmProvider[] }>(MODEL, 'ListProviders', {}, signal),
  /** `apiKey` empty keeps the stored key. */
  saveProvider: (provider: LlmProvider, apiKey = '', clearKey = false) =>
    rpc<object, { provider?: LlmProvider }>(MODEL, 'SaveProvider', { provider, apiKey, clearKey }),
  deleteProvider: (name: string) => rpc<{ name: string }, Empty>(MODEL, 'DeleteProvider', { name }),
  /** Ask the provider for its models; `apiKey` empty uses the stored key of the provider of that name. */
  discoverModels: (provider: Partial<LlmProvider>, apiKey = '') =>
    rpc<object, { models?: DiscoveredModel[] }>(MODEL, 'DiscoverModels', { provider, apiKey }),
  listCatalog: (signal?: AbortSignal) =>
    rpc<Empty, { models?: CatalogModel[]; aliases?: ModelAlias[] }>(MODEL, 'ListCatalog', {}, signal),
  saveModel: (model: CatalogModel) => rpc<{ model: CatalogModel }, { model?: CatalogModel }>(MODEL, 'SaveModel', { model }),
  deleteModel: (provider: string, model: string) => rpc<object, Empty>(MODEL, 'DeleteModel', { provider, model }),
  saveAlias: (alias: ModelAlias) => rpc<{ alias: ModelAlias }, Empty>(MODEL, 'SaveAlias', { alias }),
  deleteAlias: (alias: string) => rpc<{ alias: string }, Empty>(MODEL, 'DeleteAlias', { alias }),
};

// ---------------------------------------------------------------------------
// MCP hub: connectors (registry), MCPs and adapters (graph nodes, read here)
// ---------------------------------------------------------------------------

const MCP = 'goap.mcp.v1.McpService';

export interface ConnectorOperation {
  name?: string;
  description?: string;
  inputSchema?: Struct;
}

export interface ConnectorInfo {
  id?: string;
  version?: string;
  description?: string;
  /** JSON Schema of the parameters an adapter gives the connector */
  configSchema?: Struct;
  secretNames?: string[];
  operations?: ConnectorOperation[];
}

export interface Connector {
  info?: ConnectorInfo;
  endpoint?: string;
  lastSeen?: string;
  live?: boolean;
}

export interface McpTool {
  name?: string;
  description?: string;
  inputSchema?: Struct;
}

/** An MCP: the generic usage of a tool by an LLM (node `MCP:<name>` of the platform namespace). */
export interface Mcp {
  name?: string;
  description?: string;
  tools?: McpTool[];
}

export interface ToolMapping {
  tool?: string;
  operation?: string;
  arguments?: Struct;
  resultPath?: string;
}

/** How a unit implements an MCP with a connector (node `ADP:<unit>/<mcp>` of the organisation namespace). */
export interface Adapter {
  unit?: string;
  mcp?: string;
  connector?: string;
  config?: Struct;
  secrets?: Record<string, string>;
  tools?: ToolMapping[];
}

export interface EffectiveMcp {
  mcp?: Mcp;
  adapter?: Adapter;
  inherited?: boolean;
}

export interface HubTool {
  name?: string;
  description?: string;
  inputSchema?: Struct;
}

export const mcp = {
  listConnectors: (signal?: AbortSignal) => rpc<Empty, { connectors?: Connector[] }>(MCP, 'ListConnectors', {}, signal),
  listMcps: (signal?: AbortSignal) => rpc<Empty, { mcps?: Mcp[] }>(MCP, 'ListMcps', {}, signal),
  /** the MCPs a unit can use, each with the adapter that implements it (own or inherited); chain: unit then ancestors */
  listEffective: (unit: string, signal?: AbortSignal) =>
    rpc<{ unit: string }, { chain?: string[]; mcps?: EffectiveMcp[] }>(MCP, 'ListEffective', { unit }, signal),
  checkAdapter: (adapter: Adapter) => rpc<{ adapter: Adapter }, { warnings?: string[] }>(MCP, 'CheckAdapter', { adapter }),
  listTools: (unit = '', signal?: AbortSignal) =>
    rpc<{ unit: string }, { tools?: HubTool[]; mcps?: string[] }>(MCP, 'ListTools', { unit }, signal),
};
