// Modèle d'édition d'une méthodologie.
//
// Le formulaire manipule une copie « à plat » de la définition, plus commode à
// lier aux champs : les maps de conditions (pre / effects) deviennent des listes
// de lignes, les listes de chaînes des textes, les paramètres JSON un texte.
// `toForm` / `fromForm` convertissent entre ce modèle et le message proto.

import type { Action, Issue, Methodology, Struct } from './api';

export interface CondRow {
  cond: string;
  value: boolean;
}

export interface NodeTypeForm {
  name: string;
  description: string;
  /** propriétés séparées par des virgules */
  properties: string;
}

export interface LinkTypeForm {
  name: string;
  from: string;
  to: string;
}

export interface ConditionForm {
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

export interface ActionForm {
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
  /** paramètres (builtin) en JSON */
  params: string;
  hasExpects: boolean;
  expects: ExpectsForm;
}

export interface GoalForm {
  name: string;
  description: string;
  /** un exemple par ligne */
  examples: string;
  pre: CondRow[];
  value: number;
}

export interface MethodologyForm {
  name: string;
  version: string;
  description: string;
  nodeTypes: NodeTypeForm[];
  linkTypes: LinkTypeForm[];
  conditions: ConditionForm[];
  actions: ActionForm[];
  goals: GoalForm[];
}

export const ACTION_KINDS = ['llm', 'tool', 'human', 'builtin'] as const;
export const FOR_EACH = ['impacts', 'proposals', 'items', 'artifacts'] as const;
export const PRODUCE_OPS = ['create_node', 'update_node'] as const;

// --- constructeurs --------------------------------------------------------------

export const emptyNodeType = (): NodeTypeForm => ({ name: '', description: '', properties: '' });
export const emptyLinkType = (): LinkTypeForm => ({ name: '', from: '', to: '' });
export const emptyCondition = (): ConditionForm => ({ name: '', description: '', expr: '' });
export const emptyExpects = (): ExpectsForm => ({
  forEach: 'impacts',
  where: '',
  op: '',
  nodeType: '',
  linkType: '',
  direction: 'out',
});
export const emptyAction = (): ActionForm => ({
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
});
export const emptyGoal = (): GoalForm => ({ name: '', description: '', examples: '', pre: [], value: 1 });

export function emptyForm(): MethodologyForm {
  return {
    name: '',
    version: '0.1.0',
    description: '',
    nodeTypes: [],
    linkTypes: [],
    conditions: [],
    actions: [],
    goals: [],
  };
}

// --- conversions ------------------------------------------------------------------

function rows(m: Record<string, boolean> | undefined): CondRow[] {
  return Object.entries(m ?? {}).map(([cond, value]) => ({ cond, value: !!value }));
}

function actionToForm(a: Action): ActionForm {
  const e = a.expects;
  return {
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
  };
}

export function toForm(m: Methodology): MethodologyForm {
  return {
    name: m.name ?? '',
    version: m.version ?? '',
    description: m.description ?? '',
    nodeTypes: (m.nodeTypes ?? []).map((n) => ({
      name: n.name ?? '',
      description: n.description ?? '',
      properties: (n.properties ?? []).join(', '),
    })),
    linkTypes: (m.linkTypes ?? []).map((l) => ({ name: l.name ?? '', from: l.from ?? '', to: l.to ?? '' })),
    conditions: (m.conditions ?? []).map((c) => ({
      name: c.name ?? '',
      description: c.description ?? '',
      expr: c.expr ?? '',
    })),
    actions: (m.actions ?? []).map(actionToForm),
    goals: (m.goals ?? []).map((g) => ({
      name: g.name ?? '',
      description: g.description ?? '',
      examples: (g.examples ?? []).join('\n'),
      pre: rows(g.pre),
      value: g.value ?? 0,
    })),
  };
}

/** Copie `v` dans `o[k]` seulement si non vide (proto3 JSON omet les valeurs par défaut). */
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
 * Construit le message proto. Les erreurs détectables localement (JSON des
 * paramètres invalide) sont renvoyées comme des problèmes de validation.
 */
export function fromForm(f: MethodologyForm): { methodology: Methodology; issues: Issue[] } {
  const issues: Issue[] = [];
  const m: Methodology = {};
  put(m, 'name', f.name.trim());
  put(m, 'version', f.version.trim());
  put(m, 'description', f.description.trim());

  put(
    m,
    'nodeTypes',
    f.nodeTypes.map((n) => {
      const o: NonNullable<Methodology['nodeTypes']>[number] = {};
      put(o, 'name', n.name.trim());
      put(o, 'description', n.description.trim());
      put(
        o,
        'properties',
        n.properties
          .split(',')
          .map((p) => p.trim())
          .filter(Boolean),
      );
      return o;
    }),
  );
  put(
    m,
    'linkTypes',
    f.linkTypes.map((l) => {
      const o: NonNullable<Methodology['linkTypes']>[number] = {};
      put(o, 'name', l.name.trim());
      put(o, 'from', l.from);
      put(o, 'to', l.to);
      return o;
    }),
  );
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
      put(o, 'pre', toMap(a.pre));
      put(o, 'effects', toMap(a.effects));
      put(o, 'cost', num(a.cost));
      put(o, 'permission', a.permission.trim());
      // Champs propres au type d'action : les autres sont ignorés.
      if (a.kind === 'llm') {
        put(o, 'model', a.model.trim());
        put(o, 'prompt', a.prompt);
      } else if (a.kind === 'tool') {
        put(o, 'tool', a.tool.trim());
      } else if (a.kind === 'human') {
        put(o, 'instructions', a.instructions.trim());
      } else if (a.kind === 'builtin') {
        put(o, 'builtin', a.builtin.trim());
        if (a.params.trim()) {
          try {
            const p: unknown = JSON.parse(a.params);
            if (p === null || typeof p !== 'object' || Array.isArray(p)) throw new Error('objet attendu');
            put(o, 'params', p as Struct);
          } catch (e) {
            issues.push({
              path: `actions[${i}].params`,
              message: `Paramètres JSON invalides : ${e instanceof Error ? e.message : String(e)}`,
            });
          }
        }
      }
      if (a.hasExpects) {
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
  return { methodology: m, issues };
}

/** Conditions utilisables dans pre / effects : déclarées + `expect:<action>` générées. */
export function conditionNames(f: MethodologyForm): string[] {
  const names = f.conditions.map((c) => c.name.trim()).filter(Boolean);
  for (const a of f.actions) if (a.hasExpects && a.name.trim()) names.push(`expect:${a.name.trim()}`);
  return [...new Set(names)];
}

// --- chemins des problèmes ------------------------------------------------------------

const SEGMENT_ALIASES: Record<string, string> = {
  node_types: 'nodeTypes',
  link_types: 'linkTypes',
  for_each: 'forEach',
  node_type: 'nodeType',
};

/** Normalise un chemin serveur (« domain.node_types[0].name » → « nodeTypes[0].name »). */
export function normalizePath(path: string | undefined): string {
  return (path ?? '')
    .trim()
    .replace(/^domain\./, '')
    .replace(/[A-Za-z_]+/g, (seg) => SEGMENT_ALIASES[seg] ?? seg);
}

/** Chemin parent : « actions[0].pre.x » → « actions[0].pre » → « actions[0] » → « actions ». */
export function parentPath(path: string): string {
  const m = /^(.*)(\.[^.[\]]+|\[\d+\])$/.exec(path);
  return m ? m[1] : '';
}

// --- listes -----------------------------------------------------------------------

export function moveItem<T>(list: T[], i: number, delta: number): void {
  const j = i + delta;
  if (j < 0 || j >= list.length) return;
  const [x] = list.splice(i, 1);
  list.splice(j, 0, x);
}
