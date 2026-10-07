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
  landable?: boolean;
  /** the node was created by the change */
  created?: boolean;
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
    if (i.landable === false) props.landable = 'no';
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
    view.pane ? `pane ${view.pane}` : '',
    view.scope && view.scope !== 'main' ? `scope ${view.scope}` : '',
    view.filter && view.filter !== 'all' ? `filtered to ${view.filter}` : '',
  ];
  return parts.filter(Boolean).join('; ') + '.';
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
