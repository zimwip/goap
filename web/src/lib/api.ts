// Minimal client for the GOAP gateway (Connect protocol, JSON encoding).
// Each RPC is a `POST /{package.Service}/{Method}` with a JSON body
// (proto3 JSON: fields in lowerCamelCase, default values omitted).

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

import { COMMAND_HEADER, newCommandId } from './flux/commands';

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

/** The claims of a JWT the web reads (payload only: the gateway verifies the signature). */
export interface TokenClaims {
  sub?: string;
  exp?: number;
  iat?: number;
  auth_time?: number;
}

/** Decodes the payload of a JWT (undefined when it is not one). */
export function tokenClaims(token: string | null): TokenClaims | undefined {
  const part = token?.split('.')[1];
  if (!part) return undefined;
  try {
    const b64 = part.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(part.length / 4) * 4, '=');
    return JSON.parse(atob(b64)) as TokenClaims;
  } catch {
    return undefined;
  }
}

const unauthorizedListeners = new Set<(message: string) => void>();

/**
 * Subscribe to the requests refused for their token (401 while one was sent): the session expired or is no longer
 * valid. Returns the unsubscribe function.
 */
export function onUnauthorized(fn: (message: string) => void): () => void {
  unauthorizedListeners.add(fn);
  return () => unauthorizedListeners.delete(fn);
}

/**
 * Reports a 401 on a request that carried `sent`: ignored when the token changed meanwhile (a refresh or a new
 * sign-in raced the request), so only the current token can end the session.
 */
export function reportUnauthorized(sent: string | null, status: number, message: string): void {
  if (status !== 401 || !sent || sent !== getToken()) return;
  for (const fn of unauthorizedListeners) fn(message);
}

export async function rpc<TReq extends object, TRes>(
  service: string,
  method: string,
  body: TReq,
  signal?: AbortSignal,
): Promise<TRes> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json', [COMMAND_HEADER]: newCommandId() };
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
    reportUnauthorized(token, res.status, err.message ?? '');
    throw new RpcError(err.code ?? (res.status === 401 ? 'unauthenticated' : 'unknown'), err.message ?? (text || res.statusText), res.status);
  }
  return (data ?? {}) as TRes;
}

/** Is this the answer for an object that does not exist (any more)? */
export const isNotFound = (e: unknown): boolean => e instanceof RpcError && e.code === 'not_found';

/** Human-readable error message for the UI. */
export function errorMessage(e: unknown): string {
  if (e instanceof RpcError) {
    if (e.code === 'permission_denied')
      return `Access denied: you do not have the rights required for this operation${e.message ? ` (${e.message})` : ''}.`;
    if (e.code === 'unauthenticated') return `Authentication required: sign in again${e.message ? ` (${e.message})` : ''}.`;
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
  description?: string;
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
  description?: string;
  /** nodes may rest in an editable state (ADR 0048) */
  restInEditable?: boolean;
  initial?: string;
  states?: LifecycleState[];
  transitions?: LifecycleTransition[];
}

/** Resolved model of a node type of the catalogue ("<namespace>@<name>", ADR 0012). */
export interface TypeInfo {
  ref?: string;
  description?: string;
  /** resolved attributes, the inherited ones first */
  attributes?: AttributeInfo[];
  /** supertypes, nearest first */
  ancestors?: string[];
  lifecycle?: Lifecycle;
  changeControlled?: boolean;
  /** IDE editor of the nodes (ADR 0027) */
  editor?: string;
  /** qualified types the nodes embed through "contains" links */
  contains?: string[];
  /** node_validator instances checking a node as a whole, in call order (own and inherited) */
  nodeValidators?: string[];
}

/** Resolved model of a link type; an empty end accepts any node type. */
export interface LinkTypeInfo {
  ref?: string;
  from?: string;
  to?: string;
  /** composition link: the target is a part of the source, shown as its child */
  compose?: boolean;
  attributes?: AttributeInfo[];
}

export type AttributeType = 'string' | 'number' | 'boolean' | 'date' | 'enum' | 'json';
export type AttributeWidget = 'text' | 'textarea' | 'dropdown' | 'checkbox' | 'date';

/** Defines a property of a node type or link type: its code and what the UI needs to display and edit it. */
export interface Attribute {
  /** code: the key of the value in the properties */
  name?: string;
  label?: string;
  description?: string;
  /** absent: untyped */
  type?: AttributeType | string;
  /** absent: the usual one of the type */
  widget?: AttributeWidget | string;
  /** enum of the domain an enum attribute takes its values from */
  enum?: string;
  defaultValue?: string;
  section?: string;
  order?: number;
  tooltip?: string;
  /** the attribute is the display name of the node */
  asName?: boolean;
  /** property_validator instances (ADR 0018), in call order */
  validators?: string[];
}

export interface EnumValue {
  value?: string;
  label?: string;
}

/** A closed list of values of a domain that enum attributes refer to. */
export interface Enum {
  name?: string;
  description?: string;
  values?: EnumValue[];
}

/** A resolved attribute of a node type or link type of the catalogue. */
export interface AttributeInfo {
  attribute?: Attribute;
  /** the type that declares it when inherited */
  from?: string;
  /** values of the enum of an enum attribute */
  values?: EnumValue[];
}

export interface NodeType {
  name?: string;
  description?: string;
  /** what the nodes carry, with the validators of each (inherited by subtypes) */
  attributes?: Attribute[];
  /** parent type: the subtype inherits its attributes and link types */
  extends?: string;
  /** name of the domain lifecycle of the nodes (inherited through extends) */
  lifecycle?: string;
  /** the type embeds nodes of these types through "contains" links */
  document?: { contains?: string[] };
  /** absent: change controlled */
  changeControlled?: boolean;
  /** node_validator instances (ADR 0018) checking a node as a whole, in call order */
  validators?: string[];
  /** properties the node index keeps (ADR 0026) */
  search?: SearchProperty[];
  /** editor the UI opens the nodes with (inherited through extends; absent: the default node editor) */
  editor?: string;
}

/** How the node index uses a property: text (full text and embedding), facet (filter and count). */
export interface SearchProperty {
  property?: string;
  text?: boolean;
  facet?: boolean;
}

/** Fixed algorithm types: the extension points of the platform. */
export type AlgorithmType = 'property_validator' | 'node_validator' | 'transition_guard' | 'transition_action' | 'adapter';
export type AlgorithmParamType = 'string' | 'number' | 'boolean' | 'regex' | 'enum' | 'strings' | 'json' | 'secret';

export interface AlgorithmParam {
  name?: string;
  type?: AlgorithmParamType | string;
  description?: string;
  required?: boolean;
  defaultValue?: unknown;
  /** allowed values of an enum */
  values?: string[];
}

/** Script of a fixed type with declared parameters; declared by a domain. */
export interface Algorithm {
  name?: string;
  description?: string;
  type?: AlgorithmType | string;
  language?: ScriptLanguage | string;
  code?: string;
  params?: AlgorithmParam[];
  /** adapters only: the MCP whose tools the code implements and the connector whose operations it calls */
  mcp?: string;
  connector?: string;
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
  description?: string;
  from?: string;
  to?: string;
  /** what a link of this type carries */
  attributes?: Attribute[];
  /** composition link: the target is a part of the source, shown as its child */
  compose?: boolean;
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
  forEach?: 'changeImpacts' | 'items' | 'artifacts' | string;
  where?: string;
  produce?: ProduceSpec;
  link?: LinkSpec;
}

export type ActionKind = 'llm' | 'script' | 'tool' | 'human' | 'builtin' | 'abstract';
export type ScriptLanguage = 'javascript' | 'go';
export type PlannerKind = 'goap' | 'utility' | 'hybrid' | 'llm' | 'llm-scoring';

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
  /** roles allowed to run the action (ADR 0043), declared by the methodology; empty: those of the agent */
  roles?: string[];
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
  'process.attached',
  'step.completed',
  'change.signal',
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
  /** roles allowed to run the agent (ADR 0043), declared by the methodology; empty: any member of the project */
  roles?: string[];
  /** LLM alias the llm / llm-scoring planners call each planning cycle (required for them) */
  model?: string;
}

export interface Goal {
  name?: string;
  description?: string;
  examples?: string[];
  pre?: Record<string, boolean>;
  value?: number;
}

/**
 * Step of a process (ADR 0034): done by one method — sub-steps, an action (or alternative actions), an agent, a nested
 * process — or by hand (none: a manual step showing its instructions).
 */
export interface ProcessStep {
  name?: string;
  description?: string;
  /** what a person does, for a manual step */
  instructions?: string;
  /** entry conditions (also of its sub-steps); steps are sequenced by their conditions, not their position */
  pre?: Record<string, boolean>;
  /** exit criteria (default: derived from the method) */
  done?: Record<string, boolean>;
  /** reference documents that describe the step */
  references?: DocumentReference[];
  /** markdown: what the step is for and how to go about it */
  guidance?: string;
  /** what a person checks before marking the step done */
  checklist?: string[];
  /** what the step produces (document types) */
  deliverables?: string[];
  steps?: ProcessStep[];
  action?: string;
  /** alternative actions the planner chooses among */
  actions?: string[];
  /** nested process: "<process>" or "<methodology>/<process>" */
  process?: string;
  /** the capability the step needs done, provided by methods */
  method?: string;
  /** CEL list: the step naming a capability runs once per element, in parallel streams (ADR 0050) */
  foreach?: string;
  /** CEL key of the group of each element: one stream per group */
  groupBy?: string;
  /** roles assigned to the step (its sub-steps inherit them) */
  roles?: Responsibilities;
}

/** Process (ADR 0034): the steps that reach an objective, run by an agent of its name towards a goal of its name. */
export interface MethodologyProcess {
  name?: string;
  description?: string;
  examples?: string[];
  /** reference documents that describe the process */
  references?: DocumentReference[];
  steps?: ProcessStep[];
}

/**
 * Method (ADR 0035 §1, ADR 0050): how a step capability is carried out in a context; not an actor, it names no agent.
 */
export interface MethodologyMethod {
  name?: string;
  /** the capability it provides */
  for?: string;
  /** CEL condition of its context (empty: always) */
  when?: string;
  priority?: number;
  description?: string;
  guidance?: string;
  checklist?: string[];
  deliverables?: string[];
  references?: DocumentReference[];
  /** roles involved when the method is used (replacing those of the step) */
  roles?: Responsibilities;
  /** the method composes its own steps and sub-steps, like a process */
  steps?: ProcessStep[];
  /** the actions that realize the method: the pool of the agent applying it, with those of its steps (ADR 0050) */
  actions?: string[];
  /** exit criteria of a method without steps */
  done?: Record<string, boolean>;
  /** planner (goap by default), LLM alias and MCPs of the agent applying the method */
  planner?: string;
  model?: string;
  mcps?: string[];
}

/** A process as a graph: its steps, the edges its conditions draw, the methods of its capabilities (ADR 0036 §4). */
export interface ProcessGraph {
  process?: string;
  description?: string;
  steps?: GraphStep[];
  edges?: GraphEdge[];
  methods?: MethodologyMethod[];
  /** the goal the agent of each method reaches */
  methodGoals?: Record<string, string>;
  agents?: GraphAgent[];
  references?: DocumentReference[];
}

/** One level of a process or method: the direct steps of a parent, chained from its inputs to its outputs. */
export interface LevelCheck {
  /** the root name, or the path of a step with sub-steps */
  path?: string;
  kind?: 'process' | 'method' | 'step' | 'agent' | 'action';
  /** who operates the level (kind agent, or a method naming an agent) and the goal it plans towards */
  agent?: string;
  goal?: string;
  inputs?: Record<string, boolean>;
  outputs?: Record<string, boolean>;
  /** the direct steps in the order the conditions allow; one layer = independent steps */
  order?: { name?: string; layer?: number }[];
  gaps?: LevelGap[];
  ok?: boolean;
  /** the direct steps of the level (the system of interest) and the links their conditions draw */
  steps?: LevelNode[];
  edges?: GraphEdge[];
}

export interface LevelNode {
  name?: string;
  path?: string;
  method?: string;
  target?: string;
  capability?: string;
  /** the step runs once per element (or group) of this CEL list, in parallel streams (ADR 0050) */
  foreach?: string;
  groupBy?: string;
  entry?: Record<string, boolean>;
  exit?: Record<string, boolean>;
  /** the step has sub-steps: a level of its own */
  composite?: boolean;
  /** that level, or one below it, is not resolved */
  broken?: boolean;
  subSteps?: number;
}

export interface LevelGap {
  /** the direct step concerned (empty: the outputs of the level) */
  step?: string;
  kind?: 'blocked' | 'output' | 'external' | 'inner' | 'noop';
  missing?: string[];
  message?: string;
}

export interface GraphStep {
  path?: string;
  name?: string;
  description?: string;
  parent?: string;
  depth?: number;
  leaf?: boolean;
  method?: string;
  target?: string;
  entry?: Record<string, boolean>;
  exit?: Record<string, boolean>;
  roles?: Responsibilities;
  guidance?: string;
  references?: DocumentReference[];
  process?: string;
  capability?: string;
  foreach?: string;
  groupBy?: string;
}

/** The exit criteria of from meet the entry of to, on these conditions. */
export interface GraphEdge {
  from?: string;
  to?: string;
  conditions?: string[];
}

export interface GraphAgent {
  name?: string;
  description?: string;
  planner?: string;
  goals?: string[];
  actions?: { name?: string; kind?: string; description?: string; pre?: Record<string, boolean>; effects?: Record<string, boolean> }[];
}

/**
 * The result of planning a goal from a (possibly condition-overridden) world state, with the planner the agent is
 * actually configured with (goap, utility or hybrid); an llm/llm-scoring agent cannot be previewed.
 */
export interface PlanPreview {
  agent?: string;
  goal?: string;
  planner?: string;
  /** the goal already holds in the given world: actions is then empty */
  reached?: boolean;
  actions?: PlanStep[];
  cost?: number;
  /** conditions missing that no action of this agent establishes ("name" / "!name"); set only when no plan reaches the goal */
  awaiting?: string[];
}

export interface PlanStep {
  name?: string;
  /** the step path the action was generated from */
  step?: string;
  kind?: string;
  cost?: number;
}

/** A role a methodology needs; the organisation assigns it to users per unit ("developer@TEAM-PAY"). */
export interface MethodologyRole {
  name?: string;
  description?: string;
}

/** Roles assigned to a step or a method, RACI style (ADR 0035 §2). */
export interface Responsibilities {
  /** performs its human tasks */
  responsible?: string;
  /** may approve its gates, never on its own change */
  accountable?: string;
  consulted?: string[];
  informed?: string[];
}

/** A reference document: "doc:<key>" (a document of the graph), "<mcp>:<path>" (a document repository) or a URL. */
export interface DocumentReference {
  title?: string;
  ref?: string;
  section?: string;
}

export interface Methodology {
  name?: string;
  version?: string;
  description?: string;
  status?: MethodologyStatus | string;
  /** namespace (domain) the changes of the methodology act on; its types are "<namespace>@<NodeType>" */
  namespace?: string;
  conditions?: Condition[];
  actions?: Action[];
  goals?: Goal[];
  agents?: Agent[];
  processes?: MethodologyProcess[];
  methods?: MethodologyMethod[];
  /** the roles the processes and methods assign (ADR 0035 §2), its agents and actions are run by (ADR 0043) */
  roles?: MethodologyRole[];
  /** transverse: its processes run alongside the changes of these methodologies (ADR 0036 §3) */
  appliesTo?: string[];
  /** the built-in condition libraries it imports (ADR 0064): decisions, risks */
  imports?: string[];
  /** the events of those changes it reacts to */
  on?: { event?: string; filter?: string }[];
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
  enums?: Enum[];
  lifecycles?: Lifecycle[];
  algorithms?: Algorithm[];
  algorithmInstances?: AlgorithmInstance[];
  createdAt?: string;
  updatedAt?: string;
  publishedAt?: string;
  updatedBy?: string;
  /** built into the platform (methodology, organisation, platform): published and frozen, it changes with the code */
  builtin?: boolean;
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
  /** built into the platform: published and frozen */
  builtin?: boolean;
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
  /** namespace (domain) the changes of the methodology act on */
  namespace?: string;
}

/** Validation issue; `path` locates the field, e.g. "conditions[2].expr". */
export interface Issue {
  path?: string;
  message?: string;
  /** flow path of the process, method or step the issue is about ("<process>/<step>/<sub-step>"); empty: none */
  activity?: string;
}

// --- access -----------------------------------------------------------------

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
  decisionEvent?: { op?: string; point?: string; outcome?: string; confidence?: number; threshold?: number; human?: boolean; accept?: boolean };
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
/** A node written by a commit: created (key, type), modified (pre) or deleted (retire). */
export interface NodeEdit {
  key?: string;
  type?: string;
  pre?: NodeRef;
  props?: Struct;
  retire?: boolean;
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

/** Decodes a log entry's payload (ADR 0030) as T: a fact, a journal record or an impact event. */
export function decodeLogEntry<T>(l: LogEntry): T {
  return JSON.parse(l.payload ?? '{}') as T;
}

/** The execution journal records among log entries (ADR 0011 records, stored as journal.* entries, ADR 0030). */
export function executionsFromLog(entries: LogEntry[]): ExecutionRecord[] {
  return entries.filter((l) => l.type?.startsWith('journal.')).map((l) => decodeLogEntry<ExecutionRecord>(l));
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
  /** the caller's active project (ADR 0039), from their token */
  project?: string;
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
  /** open: the flow is an option of the change; evaluate: the evaluation */
  option?: OptionSpec;
  comment?: string;
}

/** The hypothesis of a flow opened as an option (ADR 0009 §3). */
export interface OptionSpec {
  name?: string;
  hypothesis?: string;
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
  /** adopted flows that replace the same items: an open flow that competes cannot be adopted */
  competesWith?: string[];
  /** set on an option (ADR 0032 §6): its hypothesis, status, last evaluation, and whether the change works on it */
  option?: OptionSpec;
  optionStatus?: 'exploring' | 'evaluated' | 'selected' | 'rejected' | string;
  evaluation?: string;
  active?: boolean;
}

/** A node written by at least one option, with its version on the main flow and on each option. */
export interface OptionNode {
  node?: string;
  key?: string;
  type?: string;
  main?: NodeRef;
  /** by option id (absent: not in the graph of that option) */
  options?: Record<string, NodeRef>;
  /** properties by side: 'main' or the option id */
  props?: Record<string, Struct>;
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
  decider?: 'agent' | 'human' | string;
  threshold?: number;
  maxRounds?: number;
  deadline?: string;
  openedAt?: string;
  openedBy?: string;
  status?: 'open' | 'blocked' | 'ratifying' | 'escalated' | 'decided' | string;
  rounds?: number;
  questions?: Question[];
  ruling?: Ruling;
  /** why only a person may rule it now */
  escalation?: string;
  option?: string;
  decidedAt?: string;
  decidedBy?: string;
}

export interface OptionComparison {
  level?: string;
  options?: Flow[];
  nodes?: OptionNode[];
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

/** What a write of a change impact changes: properties, lifecycle state, links, or the node retired. */
export interface ImpactWrite {
  props?: Struct;
  state?: string;
  retire?: boolean;
  addLinks?: LinkWrite[];
  removeLinks?: string[];
}

/** A link written with a node version: its type and the node version it points to. */
export interface LinkWrite {
  type: string;
  to: NodeRef;
  props?: Struct;
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

// ---------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------

const REGISTRY = 'goap.registry.v1.RegistryService';
const GRAPH = 'goap.graph.v1.GraphService';
const ENGINE = 'goap.engine.v1.EngineService';

const MODEL = 'goap.model.v1.ModelService';
const PREFERENCES = 'goap.preferences.v1.PreferencesService';
const INDEX = 'goap.index.v1.IndexService';

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
  /** a process of a methodology as edited, as a graph (ADR 0036 §4) */
  processGraph: (methodology: Methodology, process: string, signal?: AbortSignal) =>
    rpc<{ methodology: Methodology; process: string }, { graph?: ProcessGraph; issues?: Issue[] }>(REGISTRY, 'GetProcessGraph', { methodology, process }, signal),
  /** coherence of a process or method, level by level, as edited */
  checkLevels: (methodology: Methodology, root: string, signal?: AbortSignal) =>
    rpc<{ methodology: Methodology; root: string }, { levels?: LevelCheck[]; issues?: Issue[]; conditions?: Record<string, string> }>(REGISTRY, 'CheckLevels', { methodology, root }, signal),
  /**
   * Plans toward `goal` with the planner `agent` is actually configured with, from an empty blackboard whose
   * evaluated conditions `overrides` patch on top: no live Change needed. For a process, pass its name as both
   * agent and goal; for a step naming an agent or a capability, pass the step's (or chosen method's) agent/goal.
   */
  previewPlan: (methodology: Methodology, agent: string, goal: string, overrides: Record<string, boolean>, signal?: AbortSignal) =>
    rpc<{ methodology: Methodology; agent: string; goal: string; overrides: Record<string, boolean> }, { preview?: PlanPreview; issues?: Issue[] }>(
      REGISTRY,
      'PreviewPlan',
      { methodology, agent, goal, overrides },
      signal,
    ),
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

  // --- domains (one per namespace; no change / impact / proposal involved) ---
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
  /** The type catalogue in force (ADR 0012): node and link types of the published and built-in domains. */
  listTypes: (signal?: AbortSignal) =>
    rpc<Record<string, never>, { types?: TypeInfo[]; linkTypes?: LinkTypeInfo[]; domains?: Record<string, string> }>(REGISTRY, 'ListTypes', {}, signal),
  /** Tries an algorithm on a sample input; nothing is saved. */
  runAlgorithm: (algorithm: Algorithm, values: Record<string, unknown>, input: Record<string, unknown>) =>
    rpc<{ algorithm: Algorithm; values: Record<string, unknown>; input: Record<string, unknown> }, RunAlgorithmResponse>(
      REGISTRY,
      'RunAlgorithm',
      { algorithm, values, input },
    ),
};

/** The caller as the platform sees it: token principal completed by its User node (GET /api/whoami). */
export async function whoAmI(signal?: AbortSignal): Promise<Principal> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  const res = await fetch(`${BASE}/api/whoami`, { headers, signal });
  if (!res.ok) {
    const message = await errorText(res);
    reportUnauthorized(token, res.status, message);
    throw new RpcError('unauthenticated', message, res.status);
  }
  return (await res.json()) as Principal;
}

/**
 * Switches the active project (ADR 0039): reissues the token with the same subject/org/roles, pointed at
 * a different project (POST /auth/dev-token/project), and stores it — every call from here on carries it.
 * Requires a token (the dev-token / hs256 auth flow; a deployment without one has no project to switch).
 */
export async function switchProject(project: string): Promise<void> {
  const token = getToken();
  if (!token) throw new RpcError('unauthenticated', 'no active token', 401);
  const res = await fetch(`${BASE}/auth/dev-token/project`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ project }),
  });
  if (!res.ok) {
    const message = await errorText(res);
    reportUnauthorized(token, res.status, message);
    throw new RpcError(res.status === 401 ? 'unauthenticated' : 'failed', message, res.status);
  }
  const data = (await res.json()) as { token?: string };
  if (!data.token) throw new RpcError('failed', 'no token returned', res.status);
  setToken(data.token);
}

/** The message of an HTTP error response: the `message` of a JSON body (echo, Connect), else its text. */
async function errorText(res: Response): Promise<string> {
  const text = await res.text().catch(() => '');
  try {
    const m = (JSON.parse(text) as { message?: string }).message;
    if (m) return m;
  } catch {
    // not JSON
  }
  return text || res.statusText;
}

/**
 * Reissues the current token with a fresh expiry (POST /auth/refresh, local sign-in): keeps an active session
 * going. Refused (401) when the token expired, is invalid, or the session reached its maximum age.
 */
export async function refreshToken(): Promise<void> {
  const token = getToken();
  if (!token) throw new RpcError('unauthenticated', 'no active token', 401);
  let res: Response;
  try {
    res = await fetch(`${BASE}/auth/refresh`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } });
  } catch (e) {
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }
  if (!res.ok) {
    const message = await errorText(res);
    reportUnauthorized(token, res.status, message);
    throw new RpcError(res.status === 401 ? 'unauthenticated' : 'failed', message, res.status);
  }
  const data = (await res.json()) as { token?: string };
  if (!data.token) throw new RpcError('failed', 'no token returned', res.status);
  // a sign-out or another refresh meanwhile wins
  if (getToken() === token) setToken(data.token);
}

/** Which sign-in UI to show (GET /api/auth/config, unauthenticated — ADR 0040): 'none', 'hs256' or 'local'. */
export async function authConfig(signal?: AbortSignal): Promise<{ authMode: string }> {
  const res = await fetch(`${BASE}/api/auth/config`, { signal });
  if (!res.ok) throw new RpcError('failed', res.statusText, res.status);
  return (await res.json()) as { authMode: string };
}

async function authToken(path: string, subject: string, password: string): Promise<void> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ subject, password }),
  });
  if (!res.ok) throw new RpcError(res.status === 401 ? 'unauthenticated' : 'failed', await errorText(res), res.status);
  const data = (await res.json()) as { token?: string };
  if (!data.token) throw new RpcError('failed', 'no token returned', res.status);
  setToken(data.token);
}

/** Creates a local account and signs in (POST /auth/register, ADR 0040: no external identity provider). */
export const register = (subject: string, password: string): Promise<void> => authToken('/auth/register', subject, password);

/** Signs in with a local account (POST /auth/login). */
export const login = (subject: string, password: string): Promise<void> => authToken('/auth/login', subject, password);

/**
 * Signs out (POST /auth/logout, ADR 0045): the gateway ends the session of the token, so its tokens are refused
 * from now on — every session of the user with `everywhere` (all their devices) — and the local token is cleared.
 */
export async function logout(everywhere = false): Promise<void> {
  const token = getToken();
  try {
    await fetch(`${BASE}/auth/logout`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: JSON.stringify({ everywhere }),
    });
  } finally {
    setToken(null);
  }
}

export interface SharedNode {
  node?: NodeRef;
  key?: string;
  changes?: string[];
}

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
  /** Edits a change: title, intent, goal; status 'abandoned' abandons it (its sub-changes and its branch too). */
  updateChange: (id: string, patch: { title?: string; intent?: string; goal?: string; status?: string }) =>
    rpc<{ id: string; title?: string; intent?: string; goal?: string; status?: string }, { change?: Change }>(GRAPH, 'UpdateChange', { id, ...patch }),
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
  openDecision: (req: { changeId: string; question: string; allOptions: boolean; options?: string[]; criteria?: string[]; decider?: string; threshold?: number; maxRounds?: number; maxDuration?: string }) =>
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
  /** Write the next version of a change impact's node on the change branch. */
  /** addLinks / removeLinks: links of the new version (removeLinks by link id, from the version written before). */
  writeChangeImpact: (changeId: string, changeImpactId: string, w: ImpactWrite, flow = '') =>
    rpc<ImpactWrite & { changeId: string; changeImpactId: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'WriteChangeImpact', { changeId, changeImpactId, ...w, flow }),
  /** Accept or reject a change impact; the comment is mandatory. */
  /** flow: the flow or option the review is made on ('main' names the main flow; '' is the active option). */
  reviewChangeImpact: (changeId: string, changeImpactId: string, accept: boolean, comment: string, flow = '') =>
    rpc<{ changeId: string; changeImpactId: string; accept: boolean; comment: string; flow: string }, { node?: ChangeImpact }>(GRAPH, 'ReviewChangeImpact', { changeId, changeImpactId, accept, comment, flow }),
  /** The change as a flow or an option sees it: its change impacts (with the post versions of that flow) and items. */
  getBlackboard: (changeId: string, flow: string, signal?: AbortSignal) =>
    rpc<{ changeId: string; flow: string }, { change?: Change; options?: Flow[]; activeOption?: string; decisionPoints?: DecisionPoint[] }>(GRAPH, 'GetBlackboard', { changeId, flow }, signal),
  /** Create a change, write the edits on its branch, accept them and apply it (one call). */
  commitEdits: (req: { namespace: string; title: string; intent: string; baselineId: string; edits: NodeEdit[] }) =>
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
  /** Creates a data node typed by a NodeType of the methodology (published, synced on the graph). */
  createObject: (methodology: string, nodeType: string, key: string, props: Struct) =>
    rpc<
      { methodology: string; nodeType: string; key: string; props: Struct },
      { node?: GraphNode; baseline?: Baseline }
    >(GRAPH, 'CreateObject', { methodology, nodeType, key, props }),
  /** Removes a change that landed nothing, with its log (ADR 0037); refused once anything of it is applied or used. */
  deleteChange: (changeId: string) => rpc<{ changeId: string }, { change?: Change }>(GRAPH, 'DeleteChange', { changeId }),
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
  /** Unblocks a run waiting for conditions or stuck (ADR 0036 §3): waive conditions (with a reason), retry, or abandon (with a reason). */
  unblockProcess: (processId: string, decision: 'waive' | 'retry' | 'abandon', conditions: string[] = [], reason = '') =>
    rpc<{ processId: string; decision: string; conditions: string[]; reason: string }, { process?: Process }>(ENGINE, 'UnblockProcess', {
      processId,
      decision,
      conditions,
      reason,
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
  getProcessProgress: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { progress?: ProcessProgress }>(ENGINE, 'GetProcessProgress', { id }, signal),
  /** How a condition of the run's world state got its value against the run's change. */
  explainCondition: (processId: string, condition: string, signal?: AbortSignal) =>
    rpc<{ processId: string; condition: string }, ConditionExplanation>(ENGINE, 'ExplainCondition', { processId, condition }, signal),
  listProcesses: (req: ListProcessesRequest = {}, signal?: AbortSignal) =>
    rpc<ListProcessesRequest, { processes?: Process[] }>(ENGINE, 'ListProcesses', req, signal),
  /** Binds an unbound process (ADR 0031) to an existing change (changeId set) or a new one. */
  attachChange: (req: AttachChangeRequest) => rpc<AttachChangeRequest, { process?: Process }>(ENGINE, 'AttachChange', req),
  /** The process's own log (ADR 0031), independent of whether it has a change. */
  getProcessLog: (processId: string, signal?: AbortSignal) =>
    rpc<{ processId: string }, { entries?: ProcessLogEntry[] }>(ENGINE, 'GetProcessLog', { processId }, signal),
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
  reportUnauthorized(token, res.status, '');
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
  /** references an API key */
  hasKey?: boolean;
  /** where the key is: `env:<VAR>` or `<vault path>#<field>`, alternatives separated by `|`; the key itself is never stored */
  apiKeyRef?: string;
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

/** The personal preferences of the caller, kept outside the graph (ADR 0038): theme, voice input, dashboard defaults. */
export const preferencesApi = {
  get: (signal?: AbortSignal) => rpc<Empty, { values?: Struct }>(PREFERENCES, 'GetPreferences', {}, signal),
  /** Merges values in: a null value clears a key. Answers the preferences after the merge. */
  set: (values: Struct) => rpc<{ values: Struct }, { values?: Struct }>(PREFERENCES, 'SetPreferences', { values }),
  reset: () => rpc<Empty, Empty>(PREFERENCES, 'ResetPreferences', {}),
};

export const models = {
  /** Models (and aliases) the caller may use. */
  listAvailable: (signal?: AbortSignal) =>
    rpc<Empty, { models?: AvailableModel[]; aliases?: ModelAlias[] }>(MODEL, 'ListModels', {}, signal),
  listProviderKinds: (signal?: AbortSignal) =>
    rpc<Empty, { kinds?: ProviderKind[]; protocols?: { id: string; label?: string }[] }>(MODEL, 'ListProviderKinds', {}, signal),
  listProviders: (signal?: AbortSignal) => rpc<Empty, { providers?: LlmProvider[] }>(MODEL, 'ListProviders', {}, signal),
  /** Ask the provider for its models; `apiKey` empty resolves the key from the provider's reference. Read-only: the configuration is edited with `llmEdit`. */
  discoverModels: (provider: Partial<LlmProvider>, apiKey = '') =>
    rpc<object, { models?: DiscoveredModel[] }>(MODEL, 'DiscoverModels', { provider, apiKey }),
  listCatalog: (signal?: AbortSignal) =>
    rpc<Empty, { models?: CatalogModel[]; aliases?: ModelAlias[] }>(MODEL, 'ListCatalog', {}, signal),
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
  /** the tool changes nothing (a unit can keep only these) */
  readOnly?: boolean;
}

/** Where a methodology may use an MCP (ADR 0028): declared by actions, by agents only, or both. */
export type McpScope = 'action' | 'agent' | 'both';

/** An MCP: the generic usage of a tool by an LLM (node `MCP:<name>` of the platform namespace). */
export interface Mcp {
  name?: string;
  description?: string;
  tools?: McpTool[];
  /** empty: both */
  scope?: McpScope;
}

/**
 * An organisational unit's instance of an adapter of the library (an algorithm of type `adapter`): node
 * `ADP:<unit>/<mcp>` of the organisation namespace, owned by the unit. The connector and the code come from
 * the algorithm; the unit gives the parameter values (secrets as references).
 */
export interface Adapter {
  unit?: string;
  mcp?: string;
  /** name of the adapter definition (node `ADD:<name>` of the platform namespace) */
  adapter?: string;
  params?: Struct;
  /**
   * Restrictions of the MCP for the unit and its sub-units (ADR 0028); they add up along the unit chain. An
   * instance without `adapter` only restricts (the implementation is inherited).
   */
  disabled?: boolean;
  /** when not empty, the only tools allowed */
  tools?: string[];
  /** tools refused */
  deny?: string[];
  /** only the read-only tools */
  readOnly?: boolean;
}

export interface EffectiveMcp {
  mcp?: Mcp;
  adapter?: Adapter;
  inherited?: boolean;
  /** the connector the adapter calls (empty when the adapter definition cannot say) */
  connector?: string;
  /** the tools the unit may call once the restrictions of its chain apply */
  allowedTools?: string[];
  /** the units whose instance restricts the MCP, nearest first */
  restrictedBy?: string[];
  disabled?: boolean;
  /** built into the platform (goap-graph, goap-change, goap-scheduler, goap-admin) */
  builtin?: boolean;
}

export interface TemplateParam {
  name?: string;
  type?: string;
  description?: string;
  required?: boolean;
}

export interface AdapterTemplate {
  code?: string;
  params?: TemplateParam[];
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
  /** blocking problems come back as errors, the rest as warnings */
  checkAdapter: (adapter: Adapter) => rpc<{ adapter: Adapter }, { warnings?: string[] }>(MCP, 'CheckAdapter', { adapter }),
  /** skeleton of the code of an adapter between an MCP and a registered connector, and the parameters the connector needs */
  adapterTemplate: (mcpName: string, connector: string) =>
    rpc<{ mcp: string; connector: string }, AdapterTemplate>(MCP, 'AdapterTemplate', { mcp: mcpName, connector }),
  listTools: (unit = '', signal?: AbortSignal) =>
    rpc<{ unit: string }, { tools?: HubTool[]; mcps?: string[] }>(MCP, 'ListTools', { unit }, signal),
};

// ---------------------------------------------------------------------------
// Node index (ADR 0026): hybrid full-text / semantic search with facets
// ---------------------------------------------------------------------------

export interface NodeHit {
  id: string;
  version: number;
  namespace: string;
  type: string;
  key: string;
  state?: string;
  branch: string;
  main?: boolean;
  facets?: Record<string, string>;
  score?: number;
}

export interface NodeSearchRequest {
  text?: string;
  namespaces?: string[];
  types?: string[];
  states?: string[];
  branches?: string[];
  /** true: heads of main only; false: not on main; absent: every branch. */
  main?: boolean;
  facetFilters?: { name: string; values: string[] }[];
  /** Facets to count: namespace, type, state, branch, main, or a facet the node types declare. */
  facets?: string[];
  limit?: number;
  offset?: number;
}

export interface NodeSearchResult {
  hits?: NodeHit[];
  total?: number;
  facets?: { name: string; counts?: { value: string; count: number }[] }[];
  /** The embedding side took part in the ranking. */
  semantic?: boolean;
  truncated?: boolean;
}

export const nodeIndex = {
  search: (req: NodeSearchRequest, signal?: AbortSignal) => rpc<NodeSearchRequest, NodeSearchResult>(INDEX, 'Search', req, signal),
  reindex: () => rpc<object, { versions?: number }>(INDEX, 'Reindex', {}),
  status: () =>
    rpc<object, { semantic?: boolean; store?: string; nodesIndexed?: string; baselinesFollowed?: string; errors?: string }>(INDEX, 'Status', {}),
};
