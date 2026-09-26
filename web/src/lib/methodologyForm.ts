// Editing model of a methodology.
//
// The form works with a "flattened" copy of the definition, more convenient
// to bind to fields: condition maps (pre / effects) become row lists, string
// lists become text, JSON params become text. `toForm` / `fromForm` convert
// between this model and the proto message.

import type { Action, Agent, Issue, Lifecycle, LifecycleState, LifecycleTransition, LinkType, Methodology, NodeType, Struct, Trigger } from './api';

export interface CondRow {
  cond: string;
  value: boolean;
}

export interface LifecycleStateForm {
  name: string;
  description: string;
  editable: boolean;
  final: boolean;
}

export interface LifecycleTransitionForm {
  name: string;
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
  name: string;
  initial: string;
  states: LifecycleStateForm[];
  transitions: LifecycleTransitionForm[];
}

export interface NodeTypeForm {
  name: string;
  description: string;
  /** comma-separated properties */
  properties: string;
  /** parent type (subtyping) */
  extends: string;
  /** name of the domain lifecycle of the nodes ("" : none, or inherited from the parent) */
  lifecycle: string;
  /** comma-separated node types the type embeds ("" : not a document) */
  document: string;
  /** false: direct writes, outside changes */
  changeControlled: boolean;
  /** property validator instances, in call order */
  validators: { property: string; instance: string }[];
}

export interface LinkTypeForm {
  name: string;
  from: string;
  to: string;
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
  model: string;
  prompt: string;
  tool: string;
  builtin: string;
  instructions: string;
  /** params (builtin) as JSON */
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
}

export interface AgentForm extends Identified {
  name: string;
  description: string;
  /** one example per line */
  examples: string;
  planner: string;
  /** eligible actions (empty: all) */
  actions: string[];
  /** goals (empty: all) */
  goals: string[];
  triggers: TriggerForm[];
}

export interface GoalForm extends Identified {
  name: string;
  description: string;
  /** one example per line */
  examples: string;
  pre: CondRow[];
  value: number;
}

export interface MethodologyForm {
  name: string;
  version: string;
  description: string;
  /** shared domain "<name>[@<version>]"; empty: embedded node / link types */
  domainRef: string;
  nodeTypes: NodeTypeForm[];
  linkTypes: LinkTypeForm[];
  /** lifecycles of an embedded domain (edited in a shared domain; kept as they are) */
  lifecycles: LifecycleForm[];
  conditions: ConditionForm[];
  actions: ActionForm[];
  goals: GoalForm[];
  agents: AgentForm[];
}

/** Sections whose elements open in a tab. */
export type Section = 'agents' | 'actions' | 'conditions' | 'goals';
export type SectionItem = AgentForm | ActionForm | ConditionForm | GoalForm;

export const ACTION_KINDS = ['llm', 'script', 'tool', 'human', 'builtin', 'abstract'] as const;
export const SCRIPT_LANGUAGES = ['javascript', 'go'] as const;
export const PLANNERS = ['goap', 'utility', 'hybrid'] as const;
export const TRIGGER_TYPES = ['event', 'schedule'] as const;
export const TRIGGER_TARGETS = ['new_change', 'event_change'] as const;
export const FOR_EACH = ['impacts', 'proposals', 'items', 'artifacts'] as const;
export const PRODUCE_OPS = ['create_node', 'update_node'] as const;

// --- constructors --------------------------------------------------------------

export const emptyNodeType = (): NodeTypeForm => ({ name: '', description: '', properties: '', extends: '', lifecycle: '', document: '', changeControlled: true, validators: [] });
export const emptyLinkType = (): LinkTypeForm => ({ name: '', from: '', to: '' });
let uidSeq = 0;
/** New local id (elements created in the UI). */
export function newUid(): string {
  uidSeq += 1;
  return `new-${uidSeq}-${Math.random().toString(36).slice(2, 7)}`;
}

export const emptyCondition = (): ConditionForm => ({ uid: newUid(), name: '', description: '', expr: '' });
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
});
export const emptyGoal = (): GoalForm => ({ uid: newUid(), name: '', description: '', examples: '', pre: [], value: 1 });
export const emptyAgent = (): AgentForm => ({
  uid: newUid(),
  name: '',
  description: '',
  examples: '',
  planner: 'goap',
  actions: [],
  goals: [],
  triggers: [],
});
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
});

export function emptyForm(): MethodologyForm {
  return {
    name: '',
    version: '0.1.0',
    description: '',
    domainRef: '',
    nodeTypes: [],
    linkTypes: [],
    lifecycles: [],
    conditions: [],
    actions: [],
    goals: [],
    agents: [],
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
  };
}

function agentToForm(a: Agent, uid: string): AgentForm {
  return {
    uid,
    name: a.name ?? '',
    description: a.description ?? '',
    examples: (a.examples ?? []).join('\n'),
    planner: a.planner || 'goap',
    actions: [...(a.actions ?? [])],
    goals: [...(a.goals ?? [])],
    triggers: (a.triggers ?? []).map(triggerToForm),
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
    name: n.name ?? '',
    description: n.description ?? '',
    properties: (n.properties ?? []).join(', '),
    extends: n.extends ?? '',
    lifecycle: n.lifecycle ?? '',
    document: (n.document?.contains ?? []).join(', '),
    changeControlled: n.changeControlled !== false,
    validators: (n.validators ?? []).map((v) => ({ property: v.property ?? '', instance: v.instance ?? '' })),
  };
}

const csv = (v: string): string[] =>
  v
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);

export function lifecycleToForm(l: Lifecycle): LifecycleForm {
  return {
    name: l.name ?? '',
    initial: l.initial ?? '',
    states: (l.states ?? []).map((s) => ({ name: s.name ?? '', description: s.description ?? '', editable: !!s.editable, final: !!s.final })),
    transitions: (l.transitions ?? []).map((t) => ({
      name: t.name ?? '',
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
  o.states = l.states.map((s) => {
    const st: LifecycleState = { name: s.name.trim() };
    put(st, 'description', s.description.trim());
    if (s.editable) st.editable = true;
    if (s.final) st.final = true;
    return st;
  });
  o.transitions = l.transitions.map((t) => {
    const tr: LifecycleTransition = { name: t.name.trim(), from: t.from.trim(), to: t.to.trim() };
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
    name,
    initial: 'proposed',
    states: [
      { name: 'proposed', description: '', editable: false, final: false },
      { name: 'draft', description: 'Being worked on in a change', editable: true, final: false },
      { name: 'approved', description: '', editable: false, final: false },
    ],
    transitions: [
      { name: 'start', from: 'proposed', to: 'draft', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] },
      { name: 'approve', from: 'draft', to: 'approved', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] },
      { name: 'reopen', from: 'approved', to: 'draft', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] },
    ],
  };
}

export function linkTypeToForm(l: LinkType): LinkTypeForm {
  return { name: l.name ?? '', from: l.from ?? '', to: l.to ?? '' };
}

export function nodeTypeFromForm(n: NodeTypeForm): NodeType {
  const o: NodeType = {};
  put(o, 'name', n.name.trim());
  put(o, 'description', n.description.trim());
  put(o, 'extends', n.extends.trim());
  put(
    o,
    'properties',
    n.properties
      .split(',')
      .map((p) => p.trim())
      .filter(Boolean),
  );
  put(o, 'lifecycle', n.lifecycle.trim());
  const contains = csv(n.document);
  if (contains.length) o.document = { contains };
  if (!n.changeControlled) o.changeControlled = false;
  if (n.validators.length) o.validators = n.validators.map((v) => ({ property: v.property.trim(), instance: v.instance.trim() }));
  return o;
}

export function linkTypeFromForm(l: LinkTypeForm): LinkType {
  const o: LinkType = {};
  put(o, 'name', l.name.trim());
  put(o, 'from', l.from);
  put(o, 'to', l.to);
  return o;
}

export function toForm(m: Methodology): MethodologyForm {
  const cu = uids(m.conditions);
  const au = uids(m.actions);
  const gu = uids(m.goals);
  const agu = uids(m.agents);
  return {
    name: m.name ?? '',
    version: m.version ?? '',
    description: m.description ?? '',
    domainRef: m.domainRef ?? '',
    nodeTypes: (m.nodeTypes ?? []).map(nodeTypeToForm),
    linkTypes: (m.linkTypes ?? []).map(linkTypeToForm),
    lifecycles: (m.lifecycles ?? []).map(lifecycleToForm),
    conditions: (m.conditions ?? []).map((c, i) => ({
      uid: cu[i],
      name: c.name ?? '',
      description: c.description ?? '',
      expr: c.expr ?? '',
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
  };
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

  put(m, 'domainRef', f.domainRef.trim());
  put(m, 'nodeTypes', f.nodeTypes.map(nodeTypeFromForm));
  put(m, 'linkTypes', f.linkTypes.map(linkTypeFromForm));
  put(m, 'lifecycles', f.lifecycles.map(lifecycleFromForm));
  put(
    m,
    'conditions',
    f.conditions.map((c) => {
      const o: NonNullable<Methodology['conditions']>[number] = {};
      put(o, 'name', c.name.trim());
      put(o, 'description', c.description.trim());
      put(o, 'expr', c.expr.trim());
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
      put(o, 'utility', a.utility.trim());
      // Fields specific to the action type: the others are ignored.
      if (a.kind === 'llm') {
        put(o, 'model', a.model.trim());
        put(o, 'prompt', a.prompt);
      } else if (a.kind === 'script') {
        put(o, 'language', a.language);
        put(o, 'code', a.code);
      } else if (a.kind === 'tool') {
        put(o, 'tool', a.tool.trim());
      } else if (a.kind === 'human') {
        put(o, 'instructions', a.instructions.trim());
      } else if (a.kind === 'builtin') {
        put(o, 'builtin', a.builtin.trim());
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
      put(o, 'actions', [...a.actions]);
      put(o, 'goals', [...a.goals]);
      put(o, 'triggers', a.triggers.map(triggerFromForm));
      return o;
    }),
  );
  return { methodology: m, issues };
}

/** Properties of a node type: inherited ones first (nearest ancestor last), then its own. */
export function typeProperties(f: { nodeTypes: NodeTypeForm[] }, name: string): string[] {
  const chain: NodeTypeForm[] = [];
  for (let t = f.nodeTypes.find((n) => n.name.trim() === name); t && !chain.includes(t); ) {
    chain.unshift(t);
    const parent = t.extends.trim();
    t = parent ? f.nodeTypes.find((n) => n.name.trim() === parent) : undefined;
  }
  const props = chain.flatMap((n) => n.properties.split(',').map((p) => p.trim()).filter(Boolean));
  return [...new Set(props)];
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
  if (section === 'conditions') {
    for (const a of f.actions) {
      renameRows(a.pre, from, to);
      renameRows(a.effects, from, to);
    }
    for (const g of f.goals) renameRows(g.pre, from, to);
  } else if (section === 'actions') {
    for (const ag of f.agents) ag.actions = ag.actions.map((x) => (x === from ? to : x));
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
