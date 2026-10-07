// Registry types (proto3 JSON). Fields are optional because default values are omitted on serialization.

import type { Struct } from './common';

// --- registry ---------------------------------------------------------------

/** draft: editable · published: frozen (only executable one) · archived: read-only */
export type MethodologyStatus = 'draft' | 'published' | 'archived';

export interface LifecycleState {
  name?: string;
  description?: string;
  /** working state: only held through a change */
  /** a change cannot land while a node rests in this state (ADR 0078) */
  notLandable?: boolean;
  final?: boolean;
}

/** A named CEL predicate of a gate (ADR 0075 §3): a veto not met blocks, an objective not met needs a derogation. */
export interface Criterion {
  name?: string;
  expr?: string;
  description?: string;
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
  /** criteria of a gate of a change lifecycle (ADR 0075 §3): one veto unmet blocks, an objective unmet is waived by a derogation named after it */
  vetos?: Criterion[];
  objectives?: Criterion[];
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
  /** the nodes may carry properties that are no attribute of the type (strict attributes opt-out) */
  additionalProperties?: boolean;
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
  /** the nodes may carry properties that are no attribute of the type (a free-form type, strict attributes opt-out) */
  additionalProperties?: boolean;
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

/** What kind of verifier an effect needs (ADR 0075 §1). */
export type OracleKind = 'tool' | 'human' | 'model';

/** How the effect of an action is verified: the oracle, and that the verifier is not the producer (default true). */
export interface Verify {
  oracle?: OracleKind | string;
  independent?: boolean;
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
  /** how the effect is verified (ADR 0075): the kind of oracle and the independence of the verifier */
  verify?: Verify;
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
  /** default criticality of its changes: C1, C2 or C3 (ADR 0075 §3); empty: C2 */
  criticality?: string;
  /** main goal: a goal or a process of the methodology; its changes start with it (ADR 0096); empty: the first goal or process */
  goal?: string;
  /** the built-in condition libraries it imports (ADR 0064): decisions, risks, verification, derogations */
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
