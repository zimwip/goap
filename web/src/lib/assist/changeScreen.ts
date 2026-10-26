// What the change and node screens tell the assistant (ADR 0092), as plain data: the entities, the summary, the
// summary of a diff. Names and states only, never property values.
import type { AssistantEntity } from '../api';

export interface ImpactFacts {
  id: string;
  key: string;
  type?: string;
  /** created | modified */
  intent?: string;
  /** proposed | accepted | rejected */
  review?: string;
  /** lifecycle state after the change's writes */
  state?: string;
  committable?: boolean;
  /** the node was created by the change */
  created?: boolean;
  /** false: the impact modifies nothing (a candidate, a draft equal to its base), never accepted (ADR 0102) */
  modifies?: boolean;
}

/** An impact is awaiting its review (a draft not reviewed yet). */
export const awaitingFacts = (i: ImpactFacts): boolean => !i.review || i.review === 'proposed';

/** The impacts as entities, those awaiting their review first (the collector caps them). */
export function impactEntities(impacts: ImpactFacts[]): AssistantEntity[] {
  const ordered = [...impacts.filter(awaitingFacts), ...impacts.filter((i) => !awaitingFacts(i))];
  return ordered.map((i) => {
    const props: Record<string, string> = {};
    if (i.type) props.type = i.type;
    if (i.intent) props.intent = i.intent;
    if (i.state) props.nodeState = i.state;
    if (i.committable === false) props.committable = 'no';
    if (i.modifies === false) props.modifies = 'no';
    return { type: 'impact', id: i.id, label: i.key, state: i.review || 'proposed', ...(Object.keys(props).length ? { props } : {}) };
  });
}

export interface ChangeFacts {
  title?: string;
  status?: string;
  lifecycle?: string;
  state?: string;
  project?: string;
  methodology?: string;
  namespace?: string;
  parentId?: string;
  /** what the change can commit, merge and close (ADR 0102, 0166, graph.getCommitState); unknown: left out of the summary */
  commit?: { canCommit?: boolean; canMerge?: boolean; committed?: boolean; mergeInto?: string; mergeWhy?: string; canClose?: boolean; closeWhy?: string };
}

/** One sentence on a change and what its screen shows. */
export function changeSummary(c: ChangeFacts, impacts: ImpactFacts[], view: { pane?: string; scope?: string; filter?: string } = {}): string {
  const awaiting = impacts.filter(awaitingFacts).length;
  const parts = [
    `Change “${c.title ?? ''}”, ${c.status ?? 'unknown'}`,
    c.lifecycle ? `lifecycle ${c.lifecycle} in state ${c.state || 'none'}` : '',
    c.project ? `project ${c.project}` : '',
    c.methodology ? `methodology ${c.methodology}` : '',
    c.namespace ? `namespace ${c.namespace}` : '',
    c.parentId ? 'a sub-change' : '',
    `${impacts.length} impact${impacts.length === 1 ? '' : 's'}, ${awaiting} awaiting review`,
    ...commitParts(c),
    view.pane ? `pane ${view.pane}` : '',
    view.scope && view.scope !== 'main' ? `scope ${view.scope}` : '',
    view.filter && view.filter !== 'all' ? `filtered to ${view.filter}` : '',
  ];
  return parts.filter(Boolean).join('; ') + '.';
}

/** The commit, merge and close state of a change, in the words of the summary. */
function commitParts(c: ChangeFacts): string[] {
  const st = c.commit;
  if (!st) return [];
  return [
    st.committed ? 'committed on its branch' : '',
    st.canCommit ? 'commit possible' : 'nothing to commit',
    st.canMerge ? `merge possible${st.mergeInto ? ` into ${st.mergeInto}` : ''}` : `merge not possible${st.mergeWhy ? ` (${st.mergeWhy})` : ''}`,
    st.canClose === undefined ? '' : st.canClose ? 'close possible' : `close not possible${st.closeWhy ? ` (${st.closeWhy})` : ''}`,
  ];
}

export interface TransitionFacts {
  name: string;
  to: string;
  needsDecision?: boolean;
}

/** The transitions out of the current state as entities (names and target states), plus the decided points that may gate one. */
export function transitionEntities(transitions: TransitionFacts[], decisions: { id: string; label: string }[] = []): AssistantEntity[] {
  return [
    ...transitions.map((t): AssistantEntity => ({ type: 'transition', id: t.name, label: t.name, props: { to: t.to, ...(t.needsDecision ? { needsDecision: 'yes' } : {}) } })),
    ...decisions.map((d): AssistantEntity => ({ type: 'decision', id: d.id, label: d.label, state: 'decided' })),
  ];
}

const same = (a: unknown, b: unknown): boolean => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

/** The names of the properties an impact adds, changes and clears, against the version it starts from. */
export function diffSummary(base: Record<string, unknown> | undefined, after: Record<string, unknown> | undefined): string {
  const b = base ?? {};
  const a = after ?? {};
  const added = Object.keys(a).filter((k) => !(k in b) && a[k] !== undefined && a[k] !== '');
  const changed = Object.keys(a).filter((k) => k in b && !same(a[k], b[k]));
  const cleared = Object.keys(b).filter((k) => !(k in a) || a[k] === undefined);
  const out = [
    added.length ? `adds ${added.join(', ')}` : '',
    changed.length ? `changes ${changed.join(', ')}` : '',
    cleared.length ? `clears ${cleared.join(', ')}` : '',
  ].filter(Boolean);
  return out.length ? `Edits: ${out.join('; ')}.` : 'No edit of the properties.';
}

export interface StepFacts {
  id: string;
  name: string;
  kind?: string;
  why?: string[];
  produces?: string[];
  mayRun?: boolean;
  running?: boolean;
}

/** The steps possible now as entities (names, states and conditions only): the ones to propose, never an action. */
export function stepEntities(steps: StepFacts[]): AssistantEntity[] {
  return steps.map((s): AssistantEntity => {
    const props: Record<string, string> = {};
    if (s.kind) props.kind = s.kind;
    if (s.why?.length) props.why = s.why.join(', ');
    if (s.produces?.length) props.produces = s.produces.join(', ');
    return { type: 'step', id: s.id, label: s.name, state: s.running ? 'running' : s.mayRun ? 'possible' : 'not allowed', ...(Object.keys(props).length ? { props } : {}) };
  });
}
