// Editing model of a methodology.
//
// The form works with a "flattened" copy of the definition, more convenient
// to bind to fields: condition maps (pre / effects) become row lists, string
// lists become text, JSON params become text. `toForm` / `fromForm` convert
// between this model and the proto message.

import { subsToForm, subsFromForm, declaredNames, eventIssues, subscriptionIssues, emitIssues, isStandardEvent as isStandard, type SubscriptionForm, type EventForm } from './subscriptions';
import type { Action, Agent, AssessmentView, Attribute, Persistence, Enum, Issue, Lifecycle, LifecycleState, LifecycleTransition, LinkType, DocumentReference, Methodology, MethodologyGuardian, MethodologyIntegration, MethodologyMethod, MethodologyProcess, MethodologyRole, Responsibilities, NodeType, ProcessStep, SearchProperty, Struct, Trigger } from './api';

export interface CondRow {
  cond: string;
  value: boolean;
}

/**
 * The persistence of an agent or a method (ADR 0112): one instance per scope, run again for every new execution there
 * with its conversation. Off: not persistent.
 */
export interface PersistenceForm {
  on: boolean;
  /** "change" (default) or a path into the event that starts the run (event.process.methodology) */
  scope: string;
  /** conditions that close the instance once a run of it completes with them */
  until: CondRow[];
  /** exchanges with the model given back (0: the default) */
  memory: number;
}

export const emptyPersistence = (): PersistenceForm => ({ on: false, scope: '', until: [], memory: 0 });

export function persistenceToForm(p: Persistence | undefined): PersistenceForm {
  if (!p) return emptyPersistence();
  return { on: true, scope: p.scope ?? '', until: Object.entries(p.until ?? {}).map(([cond, value]) => ({ cond, value: !!value })), memory: p.memory ?? 0 };
}

export function persistenceFromForm(f: PersistenceForm): Persistence | undefined {
  if (!f.on) return undefined;
  const o: Persistence = {};
  const scope = f.scope.trim();
  if (scope && scope !== 'change') o.scope = scope;
  const until: Record<string, boolean> = {};
  for (const r of f.until) if (r.cond.trim()) until[r.cond.trim()] = r.value;
  if (Object.keys(until).length) o.until = until;
  if (f.memory > 0) o.memory = Math.trunc(f.memory);
  // an empty object still says "persistent", with the defaults: the server keeps it as such
  return o;
}

export interface LifecycleStateForm {
  name: string;
  description: string;
  notLandable: boolean;
  final: boolean;
}

export interface LifecycleTransitionForm {
  name: string;
  description: string;
  from: string;
  to: string;
  permission: string;
  guard: string;
  /** comma-separated */
  requiresAttributes: string;
  requiresLinks: string;
  /** comma-separated states allowed for the contained children */
  children: string;
  /** transition_guard instances, in call order */
  guards: string[];
  /** transition_action instances, in call order */
  actions: string[];
}

export interface LifecycleForm {
  /** local id, never sent: the tab survives renames */
  uid: string;
  name: string;
  description: string;
  initial: string;
  states: LifecycleStateForm[];
  transitions: LifecycleTransitionForm[];
}

export interface AttributeForm {
  /** local id, never sent: the editor keeps its place across renames */
  uid: string;
  /** code: the key of the value */
  name: string;
  label: string;
  description: string;
  /** "" : untyped */
  type: string;
  /** "" : the usual one of the type */
  widget: string;
  enum: string;
  default: string;
  section: string;
  order: number;
  tooltip: string;
  asName: boolean;
  /** property_validator instances, in call order */
  validators: string[];
}

export interface EnumValueForm {
  value: string;
  label: string;
}

export interface EnumForm {
  /** local id, never sent: the tab survives renames */
  uid: string;
  name: string;
  description: string;
  values: EnumValueForm[];
}

export interface NodeTypeForm {
  /** local id, never sent: the tab survives renames */
  uid: string;
  name: string;
  description: string;
  /** what the nodes carry, each with its validators */
  attributes: AttributeForm[];
  /** parent type (subtyping) */
  extends: string;
  /** name of the domain lifecycle of the nodes ("" : none, or inherited from the parent) */
  lifecycle: string;
  /** comma-separated node types the type embeds ("" : not a document) */
  document: string;
  /** false: direct writes, outside changes */
  changeControlled: boolean;
  /** true: free-form type, its nodes may carry properties that are no attribute of it (strict attributes opt-out) */
  additionalProperties: boolean;
  /** node_validator instances, in call order */
  validators: string[];
  /** editor the UI opens the nodes with ("" : inherited, or the default node editor) */
  editor: string;
  /** node index declarations (kept as they are: not edited by the form) */
  search: SearchProperty[];
}

export interface LinkTypeForm {
  /** local id, never sent: the tab survives renames */
  uid: string;
  name: string;
  description: string;
  from: string;
  to: string;
  /** what a link of this type carries */
  attributes: AttributeForm[];
  /** composition link: the target is a part of the source (shown as its child by the editors) */
  compose: boolean;
  /** link_validator instances, in call order */
  validators: string[];
  /** the link read from its source ('' : the name) */
  label: string;
  /** the link read from its target ('' : the label) */
  inverseLabel: string;
}

/**
 * Elements that can be opened in a tab (agents, actions, conditions, goals)
 * carry a stable local `uid` (never sent to the server): it survives
 * renames and reorderings as long as the draft is in memory.
 */
export interface Identified {
  uid: string;
}

export interface ConditionForm extends Identified {
  name: string;
  description: string;
  expr: string;
  /** kind of subject of a scoped condition (ADR 0109): '' (the change), impact, item, risk, action, step */
  scope: string;
  /** the subjects of a scoped condition or an aggregate (CEL, `self` the subject) */
  where: string;
  /** aggregate: the scoped condition every / some / no subject satisfies */
  all: string;
  any: string;
  none: string;
  /** value of an `all` aggregate over no subject */
  empty: boolean;
}

export interface ExpectsForm {
  forEach: string;
  where: string;
  op: string;
  nodeType: string;
  linkType: string;
  direction: string;
}

export interface ActionForm extends Identified {
  name: string;
  description: string;
  kind: string;
  pre: CondRow[];
  effects: CondRow[];
  cost: number;
  permission: string;
  /** roles allowed to run the action (ADR 0043), comma separated; empty: those of the agent */
  roles: string;
  model: string;
  prompt: string;
  tool: string;
  builtin: string;
  instructions: string;
  /** params (a builtin's, or those a specialization passes) as JSON */
  params: string;
  hasExpects: boolean;
  expects: ExpectsForm;
  /** script: javascript | go */
  language: string;
  code: string;
  /** numeric CEL expression (utility / hybrid planners) */
  utility: string;
  /** specialized action: "<action>" or "<methodology>/<action>" */
  specializes: string;
  /** CEL guard of the specialization */
  when: string;
  /** specialization priority (highest wins) */
  priority: number;
  /** effects reached over several runs */
  incremental: boolean;
  /** MCPs used by an llm / script action, comma separated */
  mcps: string;
  /** the events that make it reactive (ADR 0110) */
  on: SubscriptionForm[];
  /** the events of the methodology it raises when it reaches its effects, comma separated (ADR 0110) */
  emits: string;
  /** scoped action (ADR 0109): runs once per pending subject of this scope (impact, item...), within `where` */
  scope: string;
  where: string;
  /** subjects a scoped llm or tool action runs for at once (0: the engine's bound) */
  parallel: number;
  /** human: the form the person fills in, as JSON (ADR 0115); the code (language, code) runs on its values */
  form: string;
}

export interface TriggerForm {
  name: string;
  description: string;
  /** event | schedule */
  type: string;
  event: string;
  /** CEL filter on the event */
  filter: string;
  /** 5-field cron (UTC) */
  schedule: string;
  goal: string;
  intent: string;
  /** new_change | event_change */
  target: string;
  /** comma-separated roles */
  roles: string;
  enabled: boolean;
  /** an event cancels the runs still at work on the change (event_change) */
  supersede: boolean;
}

export interface AgentForm extends Identified {
  name: string;
  description: string;
  /** one example per line */
  examples: string;
  planner: string;
  /** LLM alias the llm / llm-scoring planners call each planning cycle (required for them) */
  model: string;
  /** eligible actions (empty: all) */
  actions: string[];
  /** goals (empty: all) */
  goals: string[];
  /** MCPs whose tools the llm / script actions of the agent may use, comma separated */
  mcps: string;
  /** roles allowed to run the agent (ADR 0043), comma separated; empty: any member of the project */
  roles: string;
  triggers: TriggerForm[];
  /** one instance per scope (ADR 0112) */
  persistent: PersistenceForm;
}

export interface GoalForm extends Identified {
  name: string;
  description: string;
  /** one example per line */
  examples: string;
  pre: CondRow[];
  value: number;
}

/** How a step is done (ADR 0034). */
export type StepMethod = 'manual' | 'steps' | 'action' | 'actions' | 'process' | 'capability';
/**
 * What a step is made of: everything is an activity, composed of sub-activities (steps, then actions, which are not broken
 * down further) and specialized into variants (methods). An agent is not a way to do a step: it is the actor of a method.
 */
export const STEP_METHODS: { id: StepMethod; label: string }[] = [
  { id: 'steps', label: 'Sub-activities (steps)' },
  { id: 'action', label: 'An action (not broken down)' },
  { id: 'actions', label: 'Alternative actions (the planner chooses)' },
  { id: 'capability', label: 'Variants: methods that specialize it (chosen by context)' },
  { id: 'process', label: 'A nested process' },
  { id: 'manual', label: 'By hand (a person, described only)' },
];

export interface StepForm {
  /** local id (never sent) */
  key: string;
  name: string;
  description: string;
  instructions: string;
  method: StepMethod;
  action: string;
  actions: string[];
  /** "<process>" or "<methodology>/<process>" */
  process: string;
  /** the capability provided by methods */
  capability: string;
  /** CEL list (capability steps): one parallel stream per element, `vars.item`; groupBy: CEL group key, one stream per group */
  foreach: string;
  groupBy: string;
  pre: CondRow[];
  done: CondRow[];
  references: ReferenceForm[];
  /** markdown: what the step is for and how to go about it */
  guidance: string;
  /** one item per line: what a person checks */
  checklist: string;
  /** comma separated: what the step produces (document types) */
  deliverables: string;
  roles: ResponsibilitiesForm;
  steps: StepForm[];
  /** the events that make it reactive (ADR 0110) */
  on: SubscriptionForm[];
  /** CEL over `self`: the subjects of the scoped action the step runs it for (ADR 0109) */
  where: string;
}

export interface ReferenceForm {
  title: string;
  ref: string;
  section: string;
}

export interface ProcessForm extends Identified {
  name: string;
  description: string;
  /** one example per line */
  examples: string;
  references: ReferenceForm[];
  steps: StepForm[];
  /** the events that make it reactive (ADR 0110) */
  on: SubscriptionForm[];
}

export interface RoleForm {
  name: string;
  description: string;
  /** operations on a change the role allows (ADR 0103), comma separated: codes of the catalogue or doer / reviewer; empty: all */
  operations: string;
}

/** RACI roles of a step or a method; consulted and informed comma separated. */
export interface ResponsibilitiesForm {
  responsible: string;
  accountable: string;
  consulted: string;
  informed: string;
}

export const emptyResponsibilities = (): ResponsibilitiesForm => ({ responsible: '', accountable: '', consulted: '', informed: '' });

function respToForm(r: Responsibilities | undefined): ResponsibilitiesForm {
  return { responsible: r?.responsible ?? '', accountable: r?.accountable ?? '', consulted: (r?.consulted ?? []).join(', '), informed: (r?.informed ?? []).join(', ') };
}

function respFromForm(r: ResponsibilitiesForm): Responsibilities | undefined {
  const o: Responsibilities = {};
  put(o, 'responsible', r.responsible.trim());
  put(o, 'accountable', r.accountable.trim());
  put(o, 'consulted', csv(r.consulted));
  put(o, 'informed', csv(r.informed));
  return Object.keys(o).length ? o : undefined;
}

export interface MethodForm extends Identified {
  name: string;
  /** the capability it provides */
  for: string;
  /** CEL condition of its context */
  when: string;
  priority: number;
  description: string;
  guidance: string;
  /** one item per line */
  checklist: string;
  /** comma separated */
  deliverables: string;
  references: ReferenceForm[];
  roles: ResponsibilitiesForm;
  /** the method composes its own steps and sub-steps, like a process */
  steps: StepForm[];
  /** the actions that realize the method: the pool of the agent applying it (ADR 0050) */
  actions: string[];
  /** exit criteria of a method without steps */
  done: CondRow[];
  /** planner of the agent applying the method, and its LLM alias (llm planners) */
  planner: string;
  model: string;
  /** comma separated */
  mcps: string;
  /** the events that make it reactive (ADR 0110) */
  on: SubscriptionForm[];
  /** the same instance of its agent carries it out at every run of its step (ADR 0112) */
  persistent: PersistenceForm;
}

export interface MethodologyForm {
  name: string;
  version: string;
  description: string;
  /** namespace (domain) the changes act on: the node types it uses are "<namespace>@<NodeType>" */
  namespace: string;
  /** main goal (ADR 0096): a goal or a process of the methodology; "" : the first goal or process */
  goal: string;
  conditions: ConditionForm[];
  actions: ActionForm[];
  goals: GoalForm[];
  agents: AgentForm[];
  processes: ProcessForm[];
  methods: MethodForm[];
  roles: RoleForm[];
  /** transverse: the methodologies it applies to, comma separated (ADR 0036 §3) */
  appliesTo: string;
  /** the condition libraries it imports, comma separated (ADR 0064) */
  imports: string;
  /** the events it reacts to; from: self (default), integrated, integrator (ADR 0106) */
  on: SubscriptionForm[];
  /** the events it declares, in addition to the standard ones (ADR 0110) */
  events: EventForm[];
  /** how the assessments of its changes read (ADR 0116), as JSON: a list of {subject, states} */
  assessments: string;
  /** the lifecycle of its domain the changes follow (ADR 0058); "" : none */
  lifecycle: string;
  /** default criticality of its changes (ADR 0075 §3), kept as is */
  criticality: string;
  /** the methodologies whose changes its changes integrate (ADR 0106) */
  integrates: IntegrationForm[];
  /** guardian.merge: what a change it integrates must satisfy to merge (CEL, with `integrator`) */
  guardianMerge: string;
  /** guardian.transitions: a guard per transition of the lifecycle of the changes it integrates */
  guardianTransitions: { name: string; expr: string }[];
}


export interface IntegrationForm {
  /** comma separated, or "*" */
  methodologies: string;
  exclusive: boolean;
  /** "" (decision), auto */
  attach: string;
}

export { SUBSCRIPTION_ORIGINS, type SubscriptionForm, type EventForm } from './subscriptions';
export const ATTACH_MODES = ['', 'auto'] as const;

/** Sections whose elements open in a tab. */
export type Section = 'agents' | 'actions' | 'conditions' | 'goals' | 'processes' | 'methods';
export type SectionItem = AgentForm | ActionForm | ConditionForm | GoalForm | ProcessForm | MethodForm;

export const ACTION_KINDS = ['llm', 'script', 'tool', 'human', 'builtin', 'abstract'] as const;
export const SCRIPT_LANGUAGES = ['javascript', 'go'] as const;
export const PLANNERS = ['goap', 'utility', 'hybrid', 'llm', 'llm-scoring'] as const;
/** Planners that call an LLM to plan: Agent.model is required for these. */
export const LLM_PLANNERS = ['llm', 'llm-scoring'] as const;
export const TRIGGER_TARGETS = ['new_change', 'event_change'] as const;
export const FOR_EACH = ['impacts', 'proposals', 'items', 'artifacts'] as const;
export const PRODUCE_OPS = ['create_node', 'update_node'] as const;

// --- constructors --------------------------------------------------------------

export const emptyNodeType = (): NodeTypeForm => ({ uid: newUid(), name: '', description: '', attributes: [], extends: '', lifecycle: '', document: '', changeControlled: true, additionalProperties: false, validators: [], editor: '', search: [] });
export const emptyLinkType = (): LinkTypeForm => ({ uid: newUid(), name: '', description: '', from: '', to: '', attributes: [], compose: false, validators: [], label: '', inverseLabel: '' });
let uidSeq = 0;
/** New local id (elements created in the UI). */
export function newUid(): string {
  uidSeq += 1;
  return `new-${uidSeq}-${Math.random().toString(36).slice(2, 7)}`;
}

export const emptyCondition = (): ConditionForm => ({ uid: newUid(), name: '', description: '', expr: '', scope: '', where: '', all: '', any: '', none: '', empty: false });
export const emptyExpects = (): ExpectsForm => ({
  forEach: 'impacts',
  where: '',
  op: '',
  nodeType: '',
  linkType: '',
  direction: 'out',
});
export const emptyAction = (): ActionForm => ({
  uid: newUid(),
  name: '',
  description: '',
  kind: 'llm',
  pre: [],
  effects: [],
  cost: 1,
  permission: '',
  roles: '',
  model: '',
  prompt: '',
  tool: '',
  builtin: '',
  instructions: '',
  params: '',
  hasExpects: false,
  expects: emptyExpects(),
  language: 'javascript',
  code: '',
  utility: '',
  specializes: '',
  when: '',
  priority: 0,
  incremental: false,
  mcps: '',
  on: [],
  emits: '',
  scope: '',
  where: '',
  parallel: 0,
  form: '',
});
export const emptyGoal = (): GoalForm => ({ uid: newUid(), name: '', description: '', examples: '', pre: [], value: 1 });
export const emptyAgent = (): AgentForm => ({
  uid: newUid(),
  name: '',
  description: '',
  examples: '',
  planner: 'goap',
  model: '',
  actions: [],
  goals: [],
  mcps: '',
  roles: '',
  triggers: [],
  persistent: emptyPersistence(),
});
export const emptyStep = (name = ''): StepForm => ({
  key: newUid(),
  name,
  description: '',
  instructions: '',
  method: 'manual',
  action: '',
  actions: [],
  process: '',
  capability: '',
  foreach: '',
  groupBy: '',
  where: '',
  pre: [],
  done: [],
  references: [],
  guidance: '',
  checklist: '',
  deliverables: '',
  roles: emptyResponsibilities(),
  steps: [],
  on: [],
});
export const emptyMethod = (): MethodForm => ({
  uid: newUid(),
  name: '',
  for: '',
  when: '',
  priority: 0,
  description: '',
  guidance: '',
  checklist: '',
  deliverables: '',
  references: [],
  roles: emptyResponsibilities(),
  steps: [],
  actions: [],
  done: [],
  planner: 'goap',
  model: '',
  mcps: '',
  on: [],
  persistent: emptyPersistence(),
});
export const emptyProcess = (): ProcessForm => ({ uid: newUid(), name: '', description: '', examples: '', references: [], steps: [emptyStep('first')], on: [] });
export const emptyTrigger = (): TriggerForm => ({
  name: '',
  description: '',
  type: 'event',
  event: 'change.created',
  filter: '',
  schedule: '',
  goal: '',
  intent: '',
  target: 'new_change',
  roles: '',
  enabled: true,
  supersede: false,
});

export function emptyForm(): MethodologyForm {
  return {
    name: '',
    version: '0.1.0',
    description: '',
    namespace: '',
    goal: '',
    conditions: [],
    actions: [],
    goals: [],
    agents: [],
    processes: [],
    methods: [],
    roles: [],
    appliesTo: '',
    imports: '',
    on: [],
    events: [],
    assessments: '',
    lifecycle: '',
    criticality: '',
    integrates: [],
    guardianMerge: '',
    guardianTransitions: [],
  };
}

// --- conversions ------------------------------------------------------------------

function rows(m: Record<string, boolean> | undefined): CondRow[] {
  return Object.entries(m ?? {}).map(([cond, value]) => ({ cond, value: !!value }));
}

/**
 * Ids of loaded elements: derived from the name (stable across reloads,
 * which allows reopening remembered tabs), deduplicated.
 */
function uids<T extends { name?: string }>(list: T[] | undefined): string[] {
  const used = new Set<string>();
  return (list ?? []).map((x, i) => {
    let u = x.name || `#${i}`;
    while (used.has(u)) u += '~';
    used.add(u);
    return u;
  });
}

const mcpList = (s: string): string[] =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

function actionToForm(a: Action, uid: string): ActionForm {
  const e = a.expects;
  return {
    uid,
    name: a.name ?? '',
    description: a.description ?? '',
    kind: a.kind || 'llm',
    pre: rows(a.pre),
    effects: rows(a.effects),
    cost: a.cost ?? 0,
    permission: a.permission ?? '',
    roles: (a.roles ?? []).join(', '),
    model: a.model ?? '',
    prompt: a.prompt ?? '',
    tool: a.tool ?? '',
    builtin: a.builtin ?? '',
    instructions: a.instructions ?? '',
    params: a.params && Object.keys(a.params).length ? JSON.stringify(a.params, null, 2) : '',
    hasExpects: !!e,
    expects: {
      forEach: e?.forEach || 'impacts',
      where: e?.where ?? '',
      op: e?.produce?.op ?? '',
      nodeType: e?.produce?.nodeType ?? '',
      linkType: e?.link?.type ?? '',
      direction: e?.link?.direction || 'out',
    },
    language: a.language || 'javascript',
    code: a.code ?? '',
    utility: a.utility ?? '',
    specializes: a.specializes ?? '',
    when: a.when ?? '',
    priority: a.priority ?? 0,
    incremental: a.incremental ?? false,
    mcps: (a.mcps ?? []).join(', '),
    on: subsToForm(a.on),
    emits: (a.emits ?? []).join(', '),
    scope: a.scope ?? '',
    where: a.where ?? '',
    parallel: a.parallel ?? 0,
    form: a.form ? JSON.stringify(a.form, null, 2) : '',
  };
}

function agentToForm(a: Agent, uid: string): AgentForm {
  return {
    uid,
    name: a.name ?? '',
    description: a.description ?? '',
    examples: (a.examples ?? []).join('\n'),
    planner: a.planner || 'goap',
    model: a.model ?? '',
    actions: [...(a.actions ?? [])],
    goals: [...(a.goals ?? [])],
    mcps: (a.mcps ?? []).join(', '),
    roles: (a.roles ?? []).join(', '),
    triggers: (a.triggers ?? []).map(triggerToForm),
    persistent: persistenceToForm(a.persistent),
  };
}

function triggerToForm(t: Trigger): TriggerForm {
  return {
    name: t.name ?? '',
    description: t.description ?? '',
    type: t.type || 'event',
    event: t.event ?? '',
    filter: t.filter ?? '',
    schedule: t.schedule ?? '',
    goal: t.goal ?? '',
    intent: t.intent ?? '',
    target: t.target || 'new_change',
    roles: (t.roles ?? []).join(', '),
    enabled: !!t.enabled,
    supersede: !!t.supersede,
  };
}

function triggerFromForm(t: TriggerForm): Trigger {
  const o: Trigger = {};
  put(o, 'name', t.name.trim());
  put(o, 'description', t.description.trim());
  put(o, 'type', t.type);
  if (t.type === 'schedule') put(o, 'schedule', t.schedule.trim());
  else {
    put(o, 'event', t.event);
    put(o, 'filter', t.filter.trim());
  }
  put(o, 'goal', t.goal);
  put(o, 'intent', t.intent.trim());
  put(o, 'target', t.target === 'new_change' ? undefined : t.target);
  if (t.supersede && t.target === 'event_change') o.supersede = true;
  put(
    o,
    'roles',
    t.roles
      .split(',')
      .map((r) => r.trim())
      .filter(Boolean),
  );
  if (t.enabled) o.enabled = true;
  return o;
}

export function nodeTypeToForm(n: NodeType): NodeTypeForm {
  return {
    uid: newUid(),
    name: n.name ?? '',
    description: n.description ?? '',
    attributes: (n.attributes ?? []).map(attributeToForm),
    extends: n.extends ?? '',
    lifecycle: n.lifecycle ?? '',
    document: (n.document?.contains ?? []).join(', '),
    changeControlled: n.changeControlled !== false,
    additionalProperties: n.additionalProperties === true,
    validators: [...(n.validators ?? [])],
    editor: n.editor ?? '',
    search: (n.search ?? []).map((s) => ({ ...s })),
  };
}

const csv = (v: string): string[] =>
  v
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);

export function lifecycleToForm(l: Lifecycle): LifecycleForm {
  return {
    uid: newUid(),
    name: l.name ?? '',
    description: l.description ?? '',
    initial: l.initial ?? '',
    states: (l.states ?? []).map((s) => ({ name: s.name ?? '', description: s.description ?? '', notLandable: !!s.notLandable, final: !!s.final })),
    transitions: (l.transitions ?? []).map((t) => ({
      name: t.name ?? '',
      description: t.description ?? '',
      from: t.from ?? '',
      to: t.to ?? '',
      permission: t.permission ?? '',
      guard: t.guard ?? '',
      requiresAttributes: (t.requiresAttributes ?? []).join(', '),
      requiresLinks: (t.requiresOutgoingLinks ?? []).join(', '),
      children: (t.childrenStates ?? []).join(', '),
      guards: [...(t.guards ?? [])],
      actions: [...(t.actions ?? [])],
    })),
  };
}

export function lifecycleFromForm(l: LifecycleForm): Lifecycle {
  const o: Lifecycle = { name: l.name.trim(), initial: l.initial.trim() };
  put(o, 'description', l.description.trim());
  o.states = l.states.map((s) => {
    const st: LifecycleState = { name: s.name.trim() };
    put(st, 'description', s.description.trim());
    if (s.notLandable) st.notLandable = true;
    if (s.final) st.final = true;
    return st;
  });
  o.transitions = l.transitions.map((t) => {
    const tr: LifecycleTransition = { name: t.name.trim(), from: t.from.trim(), to: t.to.trim() };
    put(tr, 'description', t.description.trim());
    put(tr, 'permission', t.permission.trim());
    put(tr, 'guard', t.guard.trim());
    put(tr, 'requiresAttributes', csv(t.requiresAttributes));
    put(tr, 'requiresOutgoingLinks', csv(t.requiresLinks));
    put(tr, 'childrenStates', csv(t.children));
    put(tr, 'guards', t.guards.map((g) => g.trim()).filter(Boolean));
    put(tr, 'actions', t.actions.map((a) => a.trim()).filter(Boolean));
    return tr;
  });
  return o;
}

/** A lifecycle to start from: work happens in `draft`, persisted versions rest in `approved`. */
export function defaultLifecycle(name = ''): LifecycleForm {
  return {
    uid: newUid(),
    name,
    description: '',
    initial: 'proposed',
    states: [
      { name: 'proposed', description: '', notLandable: false, final: false },
      { name: 'draft', description: 'Being worked on in a change', notLandable: true, final: false },
      { name: 'approved', description: '', notLandable: false, final: false },
    ],
    transitions: [
      { name: 'start', description: '', from: 'proposed', to: 'draft', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] },
      { name: 'approve', description: '', from: 'draft', to: 'approved', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] },
      { name: 'reopen', description: '', from: 'approved', to: 'draft', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] },
    ],
  };
}

export const emptyAttribute = (name = ''): AttributeForm => ({ uid: newUid(), name, label: '', description: '', type: '', widget: '', enum: '', default: '', section: '', order: 0, tooltip: '', asName: false, validators: [] });

export function attributeToForm(a: Attribute): AttributeForm {
  return {
    uid: newUid(),
    name: a.name ?? '',
    label: a.label ?? '',
    description: a.description ?? '',
    type: a.type ?? '',
    widget: a.widget ?? '',
    enum: a.enum ?? '',
    default: a.defaultValue ?? '',
    section: a.section ?? '',
    order: a.order ?? 0,
    tooltip: a.tooltip ?? '',
    asName: a.asName === true,
    validators: [...(a.validators ?? [])],
  };
}

export function attributeFromForm(a: AttributeForm): Attribute {
  const o: Attribute = {};
  put(o, 'name', a.name.trim());
  put(o, 'label', a.label.trim());
  put(o, 'description', a.description.trim());
  put(o, 'type', a.type);
  put(o, 'widget', a.widget);
  put(o, 'enum', a.type === 'enum' ? a.enum : '');
  put(o, 'defaultValue', a.default);
  put(o, 'section', a.section.trim());
  if (a.order) o.order = a.order;
  put(o, 'tooltip', a.tooltip.trim());
  if (a.asName) o.asName = true;
  put(o, 'validators', a.validators.map((v) => v.trim()).filter(Boolean));
  return o;
}

export const emptyEnum = (name = ''): EnumForm => ({ uid: newUid(), name, description: '', values: [{ value: '', label: '' }] });

export function enumToForm(e: Enum): EnumForm {
  return { uid: newUid(), name: e.name ?? '', description: e.description ?? '', values: (e.values ?? []).map((v) => ({ value: v.value ?? '', label: v.label ?? '' })) };
}

export function enumFromForm(e: EnumForm): Enum {
  const o: Enum = {};
  put(o, 'name', e.name.trim());
  put(o, 'description', e.description.trim());
  o.values = e.values.map((v) => (v.label.trim() ? { value: v.value.trim(), label: v.label.trim() } : { value: v.value.trim() }));
  return o;
}

export function linkTypeToForm(l: LinkType): LinkTypeForm {
  return { uid: newUid(), name: l.name ?? '', description: l.description ?? '', from: l.from ?? '', to: l.to ?? '', attributes: (l.attributes ?? []).map(attributeToForm), compose: l.compose === true, validators: [...(l.validators ?? [])], label: l.label ?? '', inverseLabel: l.inverseLabel ?? '' };
}

export function nodeTypeFromForm(n: NodeTypeForm): NodeType {
  const o: NodeType = {};
  put(o, 'name', n.name.trim());
  put(o, 'description', n.description.trim());
  put(o, 'extends', n.extends.trim());
  if (n.attributes.length) o.attributes = n.attributes.map(attributeFromForm);
  put(o, 'lifecycle', n.lifecycle.trim());
  const contains = csv(n.document);
  if (contains.length) o.document = { contains };
  if (!n.changeControlled) o.changeControlled = false;
  if (n.additionalProperties) o.additionalProperties = true;
  put(o, 'validators', n.validators.map((v) => v.trim()).filter(Boolean));
  if (n.search.length) o.search = n.search.map((s) => ({ ...s }));
  put(o, 'editor', n.editor.trim());
  return o;
}

export function linkTypeFromForm(l: LinkTypeForm): LinkType {
  const o: LinkType = {};
  put(o, 'name', l.name.trim());
  put(o, 'description', l.description.trim());
  put(o, 'from', l.from);
  put(o, 'to', l.to);
  if (l.attributes.length) o.attributes = l.attributes.map(attributeFromForm);
  if (l.compose) o.compose = true;
  put(o, 'validators', l.validators.map((v) => v.trim()).filter(Boolean));
  put(o, 'label', l.label.trim());
  put(o, 'inverseLabel', l.inverseLabel.trim());
  return o;
}

export function toForm(m: Methodology): MethodologyForm {
  const cu = uids(m.conditions);
  const au = uids(m.actions);
  const gu = uids(m.goals);
  const agu = uids(m.agents);
  const pu = uids(m.processes);
  const mu = uids(m.methods);
  return {
    name: m.name ?? '',
    version: m.version ?? '',
    description: m.description ?? '',
    namespace: m.namespace ?? '',
    goal: m.goal ?? '',
    conditions: (m.conditions ?? []).map((c, i) => ({
      uid: cu[i],
      name: c.name ?? '',
      description: c.description ?? '',
      expr: c.expr ?? '',
      scope: c.scope ?? '',
      where: c.where ?? '',
      all: c.all ?? '',
      any: c.any ?? '',
      none: c.none ?? '',
      empty: !!c.empty,
    })),
    actions: (m.actions ?? []).map((a, i) => actionToForm(a, au[i])),
    goals: (m.goals ?? []).map((g, i) => ({
      uid: gu[i],
      name: g.name ?? '',
      description: g.description ?? '',
      examples: (g.examples ?? []).join('\n'),
      pre: rows(g.pre),
      value: g.value ?? 0,
    })),
    agents: (m.agents ?? []).map((a, i) => agentToForm(a, agu[i])),
    processes: (m.processes ?? []).map((p, i) => ({
      uid: pu[i],
      name: p.name ?? '',
      description: p.description ?? '',
      examples: (p.examples ?? []).join('\n'),
      references: refsToForm(p.references),
      steps: (p.steps ?? []).map(stepToForm),
      on: subsToForm(p.on),
    })),
    methods: (m.methods ?? []).map((x, i) => ({
      uid: mu[i],
      name: x.name ?? '',
      for: x.for ?? '',
      when: x.when ?? '',
      priority: x.priority ?? 0,
      description: x.description ?? '',
      guidance: x.guidance ?? '',
      checklist: (x.checklist ?? []).join('\n'),
      deliverables: (x.deliverables ?? []).join(', '),
      references: refsToForm(x.references),
      roles: respToForm(x.roles),
      steps: (x.steps ?? []).map(stepToForm),
      actions: [...(x.actions ?? [])],
      done: rows(x.done),
      planner: x.planner || 'goap',
      model: x.model ?? '',
      mcps: (x.mcps ?? []).join(', '),
      on: subsToForm(x.on),
      persistent: persistenceToForm(x.persistent),
    })),
    roles: (m.roles ?? []).map((r) => ({ name: r.name ?? '', description: r.description ?? '', operations: (r.operations ?? []).join(', ') })),
    appliesTo: (m.appliesTo ?? []).join(', '),
    imports: (m.imports ?? []).join(', '),
    on: subsToForm(m.on),
    events: (m.events ?? []).map((e) => ({ name: e.name ?? '', description: e.description ?? '' })),
    assessments: m.assessments?.length ? JSON.stringify(m.assessments, null, 2) : '',
    lifecycle: m.lifecycle ?? '',
    criticality: m.criticality ?? '',
    integrates: (m.integrates ?? []).map((x) => ({ methodologies: (x.methodologies ?? []).join(', '), exclusive: !!x.exclusive, attach: x.attach === 'decision' ? '' : (x.attach ?? '') })),
    guardianMerge: m.guardian?.merge ?? '',
    guardianTransitions: Object.entries(m.guardian?.transitions ?? {}).map(([name, expr]) => ({ name, expr })),
  };
}

function stepMethod(s: ProcessStep): StepMethod {
  if (s.steps?.length) return 'steps';
  if (s.action) return 'action';
  if (s.actions?.length) return 'actions';
  if (s.process) return 'process';
  if (s.method) return 'capability';
  return 'manual';
}

function stepToForm(s: ProcessStep): StepForm {
  return {
    key: newUid(),
    name: s.name ?? '',
    description: s.description ?? '',
    instructions: s.instructions ?? '',
    method: stepMethod(s),
    action: s.action ?? '',
    actions: [...(s.actions ?? [])],
    process: s.process ?? '',
    capability: s.method ?? '',
    foreach: s.foreach ?? '',
    groupBy: s.groupBy ?? '',
    where: s.where ?? '',
    pre: rows(s.pre),
    done: rows(s.done),
    references: refsToForm(s.references),
    guidance: s.guidance ?? '',
    checklist: (s.checklist ?? []).join('\n'),
    deliverables: (s.deliverables ?? []).join(', '),
    roles: respToForm(s.roles),
    steps: (s.steps ?? []).map(stepToForm),
    on: subsToForm(s.on),
  };
}

function refsToForm(rs: DocumentReference[] | undefined): ReferenceForm[] {
  return (rs ?? []).map((r) => ({ title: r.title ?? '', ref: r.ref ?? '', section: r.section ?? '' }));
}

function refsFromForm(rs: ReferenceForm[]): DocumentReference[] {
  return rs.map((r) => {
    const o: DocumentReference = {};
    put(o, 'title', r.title.trim());
    put(o, 'ref', r.ref.trim());
    put(o, 'section', r.section.trim());
    return o;
  });
}

/**
 * Conditions of the steps done once they have run ("step:<process>/<path>"): manual steps and processes of other
 * methodologies without exit criteria. Any step may name them in its entry conditions.
 */
export function stepConditionNames(f: MethodologyForm): string[] {
  const out: string[] = [];
  for (const p of f.processes) {
    for (const x of walkSteps(p.steps)) {
      const s = x.step;
      const once = s.method === 'manual' || (s.method === 'process' && s.process.includes('/') && !s.process.startsWith(`${f.name.trim()}/`));
      if (once && !s.done.some((r) => r.cond.trim())) out.push(`step:${p.name}/${x.path}`);
    }
  }
  return out;
}

/** The step as the server expects it: only the fields of its method. */
export function stepFromForm(s: StepForm): ProcessStep {
  const o: ProcessStep = {};
  put(o, 'name', s.name.trim());
  put(o, 'description', s.description.trim());
  put(o, 'pre', toMap(s.pre));
  put(o, 'done', toMap(s.done));
  put(o, 'references', refsFromForm(s.references));
  put(o, 'guidance', s.guidance.trim());
  put(
    o,
    'checklist',
    s.checklist
      .split('\n')
      .map((x) => x.trim())
      .filter(Boolean),
  );
  put(o, 'deliverables', csv(s.deliverables));
  put(o, 'roles', respFromForm(s.roles));
  put(o, 'on', subsFromForm(s.on));
  if (s.method === 'action' || s.method === 'actions') put(o, 'where', s.where.trim());
  switch (s.method) {
    case 'manual':
      put(o, 'instructions', s.instructions.trim());
      break;
    case 'steps':
      put(o, 'steps', s.steps.map(stepFromForm));
      break;
    case 'action':
      put(o, 'action', s.action.trim());
      break;
    case 'actions':
      put(o, 'actions', s.actions.filter(Boolean));
      break;
    case 'process':
      put(o, 'process', s.process.trim());
      break;
    case 'capability':
      put(o, 'method', s.capability.trim());
      put(o, 'foreach', s.foreach.trim());
      put(o, 'groupBy', s.foreach.trim() ? s.groupBy.trim() : '');
      break;
  }
  return o;
}

/** Every step of a tree, with its path ("analysis/scope") and its issue path ("steps[1].steps[0]"). */
export function walkSteps(steps: StepForm[], prefix = '', at = 'steps'): { step: StepForm; path: string; at: string }[] {
  return steps.flatMap((s, i) => {
    const path = prefix ? `${prefix}/${s.name}` : s.name;
    const here = `${at}[${i}]`;
    return [{ step: s, path, at: here }, ...walkSteps(s.steps, path, `${here}.steps`)];
  });
}

/** Copies `v` into `o[k]` only if non-empty (proto3 JSON omits default values). */
function put<T extends object, K extends keyof T>(o: T, k: K, v: T[K] | undefined): void {
  if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) return;
  if (typeof v === 'object' && v !== null && !Array.isArray(v) && Object.keys(v).length === 0) return;
  o[k] = v;
}

function toMap(rs: CondRow[]): Record<string, boolean> {
  const m: Record<string, boolean> = {};
  for (const r of rs) if (r.cond.trim()) m[r.cond.trim()] = r.value;
  return m;
}

function num(n: number): number | undefined {
  return Number.isFinite(n) && n !== 0 ? n : undefined;
}

/**
 * Builds the proto message. Locally detectable errors (invalid params JSON)
 * are returned as validation issues.
 */
export function fromForm(f: MethodologyForm): { methodology: Methodology; issues: Issue[] } {
  const issues: Issue[] = [];
  const m: Methodology = {};
  put(m, 'name', f.name.trim());
  put(m, 'version', f.version.trim());
  put(m, 'description', f.description.trim());

  put(m, 'namespace', f.namespace.trim());
  put(m, 'goal', f.goal.trim());
  put(
    m,
    'conditions',
    f.conditions.map((c) => {
      const o: NonNullable<Methodology['conditions']>[number] = {};
      put(o, 'name', c.name.trim());
      put(o, 'description', c.description.trim());
      put(o, 'expr', c.expr.trim());
      put(o, 'scope', c.scope.trim());
      put(o, 'where', c.where.trim());
      put(o, 'all', c.all.trim());
      put(o, 'any', c.any.trim());
      put(o, 'none', c.none.trim());
      if (c.empty) o.empty = true;
      return o;
    }),
  );
  put(
    m,
    'actions',
    f.actions.map((a, i) => {
      const o: Action = {};
      put(o, 'name', a.name.trim());
      put(o, 'description', a.description.trim());
      put(o, 'kind', a.kind);
      const specializes = a.specializes.trim();
      put(o, 'specializes', specializes);
      if (specializes) {
        // A specialization inherits the pre / effects / expects / cost of the specialized action.
        put(o, 'when', a.when.trim());
        put(o, 'priority', num(Math.trunc(a.priority)));
      } else {
        put(o, 'pre', toMap(a.pre));
        put(o, 'effects', toMap(a.effects));
        put(o, 'cost', num(a.cost));
        if (a.incremental) o.incremental = true;
      }
      put(o, 'permission', a.permission.trim());
      put(o, 'roles', mcpList(a.roles));
      put(o, 'utility', a.utility.trim());
      // Fields specific to the action type: the others are ignored.
      if (a.kind === 'llm') {
        put(o, 'model', a.model.trim());
        put(o, 'prompt', a.prompt);
        put(o, 'mcps', mcpList(a.mcps));
      } else if (a.kind === 'script') {
        put(o, 'language', a.language);
        put(o, 'code', a.code);
        put(o, 'mcps', mcpList(a.mcps));
      } else if (a.kind === 'tool') {
        put(o, 'tool', a.tool.trim());
      } else if (a.kind === 'human') {
        put(o, 'instructions', a.instructions.trim());
        if (a.form.trim()) {
          try {
            const f: unknown = JSON.parse(a.form);
            if (f === null || typeof f !== 'object' || Array.isArray(f)) throw new Error('object expected');
            o.form = f as NonNullable<Action['form']>;
          } catch (e) {
            issues.push({ path: `actions[${i}].form`, message: `Invalid form JSON: ${e instanceof Error ? e.message : String(e)}` });
          }
        }
        // the code of a human action runs on the values of its form (ADR 0115)
        if (a.code.trim()) {
          put(o, 'language', a.language);
          put(o, 'code', a.code);
        }
      } else if (a.kind === 'builtin') {
        put(o, 'builtin', a.builtin.trim());
      }
      {
        if (a.params.trim()) {
          try {
            const p: unknown = JSON.parse(a.params);
            if (p === null || typeof p !== 'object' || Array.isArray(p)) throw new Error('object expected');
            put(o, 'params', p as Struct);
          } catch (e) {
            issues.push({
              path: `actions[${i}].params`,
              message: `Invalid params JSON: ${e instanceof Error ? e.message : String(e)}`,
            });
          }
        }
      }
      if (a.hasExpects && !specializes) {
        const x = a.expects;
        const e: NonNullable<Action['expects']> = {};
        put(e, 'forEach', x.forEach);
        put(e, 'where', x.where.trim());
        if (x.op) {
          e.produce = { op: x.op };
          put(e.produce, 'nodeType', x.nodeType);
        }
        if (x.linkType) {
          e.link = { type: x.linkType };
          put(e.link, 'direction', x.direction === 'in' ? 'in' : undefined);
        }
        o.expects = e;
      }
      put(o, 'on', subsFromForm(a.on));
      put(o, 'emits', csv(a.emits));
      put(o, 'scope', a.scope.trim());
      if (a.scope.trim()) {
        put(o, 'where', a.where.trim());
        if (a.parallel > 0) o.parallel = a.parallel;
      }
      return o;
    }),
  );
  put(
    m,
    'goals',
    f.goals.map((g) => {
      const o: NonNullable<Methodology['goals']>[number] = {};
      put(o, 'name', g.name.trim());
      put(o, 'description', g.description.trim());
      put(
        o,
        'examples',
        g.examples
          .split('\n')
          .map((x) => x.trim())
          .filter(Boolean),
      );
      put(o, 'pre', toMap(g.pre));
      put(o, 'value', num(g.value));
      return o;
    }),
  );
  put(
    m,
    'agents',
    f.agents.map((a) => {
      const o: Agent = {};
      put(o, 'name', a.name.trim());
      put(o, 'description', a.description.trim());
      put(
        o,
        'examples',
        a.examples
          .split('\n')
          .map((x) => x.trim())
          .filter(Boolean),
      );
      put(o, 'planner', a.planner);
      put(o, 'model', a.model.trim());
      put(o, 'actions', [...a.actions]);
      put(o, 'goals', [...a.goals]);
      put(o, 'mcps', mcpList(a.mcps));
      put(o, 'roles', mcpList(a.roles));
      put(o, 'triggers', a.triggers.map(triggerFromForm));
      const pe = persistenceFromForm(a.persistent);
      if (pe) o.persistent = pe;
      return o;
    }),
  );
  put(
    m,
    'processes',
    f.processes.map((p) => {
      const o: MethodologyProcess = {};
      put(o, 'name', p.name.trim());
      put(o, 'description', p.description.trim());
      put(
        o,
        'examples',
        p.examples
          .split('\n')
          .map((x) => x.trim())
          .filter(Boolean),
      );
      put(o, 'references', refsFromForm(p.references));
      put(o, 'steps', p.steps.map(stepFromForm));
      put(o, 'on', subsFromForm(p.on));
      return o;
    }),
  );
  put(
    m,
    'methods',
    f.methods.map((x) => {
      const o: MethodologyMethod = {};
      put(o, 'name', x.name.trim());
      put(o, 'for', x.for.trim());
      put(o, 'when', x.when.trim());
      put(o, 'priority', num(Math.trunc(x.priority)));
      put(o, 'description', x.description.trim());
      put(o, 'guidance', x.guidance.trim());
      put(
        o,
        'checklist',
        x.checklist
          .split('\n')
          .map((y) => y.trim())
          .filter(Boolean),
      );
      put(o, 'deliverables', csv(x.deliverables));
      put(o, 'references', refsFromForm(x.references));
      put(o, 'roles', respFromForm(x.roles));
      put(o, 'steps', x.steps.map(stepFromForm));
      put(o, 'actions', [...x.actions]);
      put(o, 'done', toMap(x.done));
      put(o, 'planner', x.planner === 'goap' ? '' : x.planner);
      put(o, 'model', x.model.trim());
      put(o, 'mcps', csv(x.mcps));
      put(o, 'on', subsFromForm(x.on));
      const pe = persistenceFromForm(x.persistent);
      if (pe) o.persistent = pe;
      return o;
    }),
  );
  put(m, 'appliesTo', csv(f.appliesTo));
  put(m, 'imports', csv(f.imports));
  put(m, 'on', subsFromForm(f.on));
  put(
    m,
    'events',
    f.events
      .filter((e) => e.name.trim())
      .map((e) => {
        const o: { name?: string; description?: string } = { name: e.name.trim() };
        put(o, 'description', e.description.trim());
        return o;
      }),
  );
  issues.push(...activityEventIssues(f));
  if (f.assessments.trim()) {
    try {
      const v: unknown = JSON.parse(f.assessments);
      if (!Array.isArray(v)) throw new Error('a list of {subject, states} expected');
      m.assessments = v as AssessmentView[];
    } catch (e) {
      issues.push({ path: 'assessments', message: `Invalid assessments JSON: ${e instanceof Error ? e.message : String(e)}` });
    }
  }
  put(m, 'lifecycle', f.lifecycle.trim());
  put(m, 'criticality', f.criticality.trim());
  put(
    m,
    'integrates',
    f.integrates
      .filter((x) => csv(x.methodologies).length || x.exclusive || x.attach)
      .map((x) => {
        const o: MethodologyIntegration = {};
        put(o, 'methodologies', csv(x.methodologies));
        if (x.exclusive) o.exclusive = true;
        put(o, 'attach', x.attach.trim());
        return o;
      }),
  );
  const transitions: Record<string, string> = {};
  for (const t of f.guardianTransitions) if (t.name.trim() && t.expr.trim()) transitions[t.name.trim()] = t.expr.trim();
  if (f.guardianMerge.trim() || Object.keys(transitions).length) {
    const g: MethodologyGuardian = {};
    put(g, 'merge', f.guardianMerge.trim());
    if (Object.keys(transitions).length) g.transitions = transitions;
    m.guardian = g;
  }
  put(
    m,
    'roles',
    f.roles.map((r) => {
      const o: MethodologyRole = {};
      put(o, 'name', r.name.trim());
      put(o, 'description', r.description.trim());
      put(o, 'operations', csv(r.operations));
      return o;
    }),
  );
  return { methodology: m, issues };
}

/**
 * The events the methodology follows and raises, checked as the compiler does (ADR 0110): every `on` and event trigger
 * names a standard event or a declared one, every `emits` a declared one, the declared names are well formed.
 */
export function activityEventIssues(f: MethodologyForm): Issue[] {
  const declared = declaredNames(f.events);
  const out = [...eventIssues(f.events), ...subscriptionIssues('', f.on, declared)];
  const steps = (list: StepForm[], at: string) => {
    for (const x of walkSteps(list, '', `${at}.steps`)) out.push(...subscriptionIssues(x.at, x.step.on, declared));
  };
  f.processes.forEach((p, i) => {
    out.push(...subscriptionIssues(`processes[${i}]`, p.on, declared));
    steps(p.steps, `processes[${i}]`);
  });
  f.methods.forEach((me, i) => {
    out.push(...subscriptionIssues(`methods[${i}]`, me.on, declared));
    steps(me.steps, `methods[${i}]`);
  });
  f.actions.forEach((a, i) => {
    out.push(...subscriptionIssues(`actions[${i}]`, a.on, declared), ...emitIssues(`actions[${i}]`, csv(a.emits), declared));
  });
  f.agents.forEach((ag, i) =>
    ag.triggers.forEach((t, j) => {
      if (t.type === 'event' && t.event.trim() && !declared.includes(t.event.trim()) && !isStandard(t.event.trim()))
        out.push({ path: `agents[${i}].triggers[${j}].event`, message: `unknown event "${t.event.trim()}": one of the standard events or an event the methodology declares (events)` });
    }),
  );
  return out;
}

/** Conditions usable in pre / effects: declared + generated `expect:<action>`. */
export function conditionNames(f: MethodologyForm): string[] {
  const names = f.conditions.map((c) => c.name.trim()).filter(Boolean);
  for (const a of f.actions)
    if (a.hasExpects && !a.specializes.trim() && a.name.trim()) names.push(`expect:${a.name.trim()}`);
  return [...new Set(names)];
}

/** Replaces a renamed reference in a condition map (order preserved). */
function renameRows(rs: CondRow[], from: string, to: string): void {
  for (const r of rs) if (r.cond === from) r.cond = to;
}

/**
 * Propagates the rename of an element to its references: conditions in the
 * pre / effects of actions and goals, actions and goals in agents.
 */
export function renameReferences(f: MethodologyForm, section: Section, from: string, to: string): void {
  if (!from || !to || from === to) return;
  const steps = f.processes.flatMap((p) => walkSteps(p.steps).map((x) => x.step));
  if (section === 'conditions') {
    for (const a of f.actions) {
      renameRows(a.pre, from, to);
      renameRows(a.effects, from, to);
    }
    for (const g of f.goals) renameRows(g.pre, from, to);
    for (const x of f.methods) renameRows(x.done, from, to);
    for (const s of steps) {
      renameRows(s.pre, from, to);
      renameRows(s.done, from, to);
    }
  } else if (section === 'actions') {
    for (const ag of f.agents) ag.actions = ag.actions.map((x) => (x === from ? to : x));
    for (const x of f.methods) x.actions = x.actions.map((y) => (y === from ? to : y));
    for (const s of steps) {
      if (s.action === from) s.action = to;
      s.actions = s.actions.map((x) => (x === from ? to : x));
    }
    // local specializations ("<action>" or "<this methodology>/<action>")
    const self = f.name.trim();
    for (const a of f.actions) {
      if (a.specializes === from) a.specializes = to;
      else if (self && a.specializes === `${self}/${from}`) a.specializes = `${self}/${to}`;
    }
    const ef = `expect:${from}`;
    renameReferences(f, 'conditions', ef, `expect:${to}`);
  } else if (section === 'goals') {
    for (const ag of f.agents) {
      ag.goals = ag.goals.map((x) => (x === from ? to : x));
      for (const t of ag.triggers) if (t.goal === from) t.goal = to;
    }
  } else if (section === 'processes') {
    const self = f.name.trim();
    for (const s of steps) {
      if (s.process === from) s.process = to;
      else if (self && s.process === `${self}/${from}`) s.process = `${self}/${to}`;
    }
  }
}

// --- issue paths ------------------------------------------------------------

const SEGMENT_ALIASES: Record<string, string> = {
  node_types: 'nodeTypes',
  link_types: 'linkTypes',
  for_each: 'forEach',
  node_type: 'nodeType',
};

/** Normalizes a server path ("domain.node_types[0].name" → "nodeTypes[0].name"). */
export function normalizePath(path: string | undefined): string {
  return (path ?? '')
    .trim()
    .replace(/^domain\./, '')
    .replace(/[A-Za-z_]+/g, (seg) => SEGMENT_ALIASES[seg] ?? seg);
}

/** Parent path: "actions[0].pre.x" → "actions[0].pre" → "actions[0]" → "actions". */
export function parentPath(path: string): string {
  const m = /^(.*)(\.[^.[\]]+|\[\d+\])$/.exec(path);
  return m ? m[1] : '';
}

// --- lists -----------------------------------------------------------------------

export function moveItem<T>(list: T[], i: number, delta: number): void {
  const j = i + delta;
  if (j < 0 || j >= list.length) return;
  const [x] = list.splice(i, 1);
  list.splice(j, 0, x);
}

/** The names a methodology can take as its main goal (ADR 0096): its goals, then its processes (each reaches the goal of its name). */
export function goalChoices(f: Pick<MethodologyForm, 'goals' | 'processes'>): string[] {
  const out: string[] = [];
  for (const n of [...f.goals.map((g) => g.name), ...f.processes.map((p) => p.name)]) {
    const name = n.trim();
    if (name && !out.includes(name)) out.push(name);
  }
  return out;
}
