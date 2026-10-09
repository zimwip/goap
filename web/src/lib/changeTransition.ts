// The lifecycle of a change and its transitions (ADR 0058), as the change tab and the assistant's `transition_change`
// share them: which transitions leave the current state, what the definition says of their gate, which decision points
// may gate one, and the single function that takes a transition. The methodology names the lifecycle, the engine moves
// the state and records it as change objects of the change (execution@State, execution@Transition: ADR 0098); the
// change itself knows none. The engine is the authority (CEL guard, vetos, objectives, `change:transition`): nothing
// here evaluates a guard, it only shows what the definition says.
import { errorMessage, registry, type Change, type ChangeObject, type DecisionPoint, type Lifecycle, type LifecycleTransition } from './api';

/** The change object types the engine records the lifecycle of a change with (the built-in domain execution). */
export const STATE_TYPE = 'execution@State';
export const TRANSITION_TYPE = 'execution@Transition';

/** Where a change is in the lifecycle of its methodology. */
export interface ChangeLifecycleState {
  /** the lifecycle the methodology of the change names; '' when it names none */
  lifecycle: string;
  /** the state the engine recorded, else the initial state of the lifecycle */
  state: string;
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '');

/** The state of a change: the execution@State object the engine wrote, else the initial state of the lifecycle the
 * methodology names (nothing is recorded before the first move or run). */
export function lifecycleState(objects: ChangeObject[] | undefined, lifecycle: string, lc?: Lifecycle): ChangeLifecycleState {
  if (!lifecycle) return { lifecycle: '', state: '' };
  const rec = (objects ?? []).find((o) => o.type === STATE_TYPE && !o.workspace);
  const state = str(rec?.value?.state);
  return { lifecycle, state: state || lc?.initial || '' };
}

export interface TransitionOffer {
  name: string;
  from: string;
  to: string;
  description: string;
  /** the guard reads change.decision: a decided decision point must be picked */
  needsDecision: boolean;
  /** what the definition asks, in one line ("" when the transition is free) */
  gate: string;
  /** the "type:action" permission the definition names, "" for the default */
  permission: string;
}

/** The change can take a transition: it follows a lifecycle and is neither committed, applied nor abandoned. */
export function movable(c: { lifecycle?: string; status?: Change['status'] | string } | undefined): boolean {
  return !!c?.lifecycle && !['applied', 'abandoned', 'committed'].includes(c.status ?? '');
}

/** The guard of the transition reads the decision point that gates it. */
export const needsDecision = (t: Pick<LifecycleTransition, 'guard'>): boolean => /\bchange\.decision\b/.test(t.guard ?? '');

/** One line on the gate of a transition: its guard, vetos and objectives, as the definition writes them. */
export function gateSummary(t: LifecycleTransition): string {
  const names = (l: LifecycleTransition['vetos']) => (l ?? []).map((c) => c.name || c.expr || '').filter(Boolean).join(', ');
  return [
    t.guard ? `guard: ${t.guard}` : '',
    t.vetos?.length ? `vetos: ${names(t.vetos)}` : '',
    t.objectives?.length ? `objectives: ${names(t.objectives)}` : '',
  ]
    .filter(Boolean)
    .join('; ');
}

/** The transitions of the lifecycle that leave `state`, in definition order. */
export function availableTransitions(lc: Lifecycle | undefined, state: string | undefined): TransitionOffer[] {
  return (lc?.transitions ?? [])
    .filter((t) => !!t.name && t.from === (state ?? ''))
    .map((t) => ({
      name: t.name ?? '',
      from: t.from ?? '',
      to: t.to ?? '',
      description: t.description ?? '',
      needsDecision: needsDecision(t),
      gate: gateSummary(t),
      permission: t.permission ?? '',
    }));
}

/** The decision points a transition already consumed (the execution@Transition objects record the one that gated
 * each move). */
export function consumedDecisions(objects: ChangeObject[] | undefined): Set<string> {
  const out = new Set<string>();
  for (const o of objects ?? []) {
    const d = o.type === TRANSITION_TYPE ? str(o.value?.decision) : '';
    if (d) out.add(d);
  }
  return out;
}

/** The decided decision points that no transition has used yet: the ones that may gate a transition. */
export function pickableDecisions(points: DecisionPoint[] | undefined, objects: ChangeObject[] | undefined): DecisionPoint[] {
  const used = consumedDecisions(objects);
  return (points ?? []).filter((p) => p.status === 'decided' && !!p.id && !used.has(p.id));
}

export const decisionLabel = (p: DecisionPoint): string => `${p.question || p.id}${p.option ? ` → ${p.option}` : ''}`;

/** What the confirmation says before a transition (the person or the assistant's card confirms it). */
export function confirmMessage(t: Pick<TransitionOffer, 'name' | 'from' | 'to'>, decision?: DecisionPoint, gate?: string): string {
  return [
    `Move the change from “${t.from}” to “${t.to}” (${t.name})?`,
    decision ? `It uses the decided point ${decisionLabel(decision)}, which it consumes.` : '',
    gate ? `Gate: ${gate}.` : '',
    `Leaving “${t.from}” freezes the impacts written in it; going back to an earlier state reopens the reviews of what was written after (ADR 0058).`,
  ]
    .filter(Boolean)
    .join('\n');
}

export interface Refusal {
  /** the whole message of the server */
  message: string;
  /** the vetos that blocked the transition */
  vetoed: string[];
  /** the objectives neither met nor covered by a derogation */
  unmet: string[];
  /** the CEL guard that was not satisfied */
  guard: string;
}

const list = (s: string) => s.split(',').map((x) => x.trim()).filter(Boolean);

/** Reads what the server says of a refused transition (the error carries a message, not structured details). */
export function parseRefusal(message: string): Refusal {
  const veto = /vetoed by ([^:]+?)(?::|$)/.exec(message);
  const unmet = /not covered by a derogation: ([^:]+?)(?::|$)/.exec(message);
  const guard = /gate not satisfied \((.*)\)(?::|$)/.exec(message);
  return { message, vetoed: veto ? list(veto[1]) : [], unmet: unmet ? list(unmet[1]) : [], guard: guard?.[1] ?? '' };
}

export interface TransitionDeps {
  confirm: (o: { message: string; title?: string; confirmLabel?: string }) => Promise<boolean>;
  call: (changeId: string, transition: string, decision: string) => Promise<{ state?: string }>;
  /** read the change again (and what depends on it) */
  refresh: () => Promise<void>;
  notify: (message: string, kind: 'ok' | 'error') => void;
}

export interface TransitionRun {
  change: Pick<Change, 'id' | 'status'> & Partial<ChangeLifecycleState>;
  offer: TransitionOffer;
  decision?: DecisionPoint;
  /** the assistant's proposal card was the confirmation: no second dialog */
  skipConfirm?: boolean;
}

export type TransitionResult = { ok: true; state: string } | { ok: false; cancelled?: boolean; refusal?: Refusal; message: string };

/** Takes a transition: the checks the server will repeat, the confirmation unless skipped, the call, the refresh. */
export async function runTransition(deps: TransitionDeps, run: TransitionRun): Promise<TransitionResult> {
  const { change, offer } = run;
  if (!change.id) return { ok: false, message: 'No change is open.' };
  if (!movable(change)) return { ok: false, message: `The change is ${change.status}: its lifecycle no longer moves.` };
  if (offer.needsDecision && !run.decision) return { ok: false, message: `The transition ${offer.name} needs a decided decision point.` };
  if (!run.skipConfirm && !(await deps.confirm({ title: `Transition ${offer.name}`, message: confirmMessage(offer, run.decision, offer.gate), confirmLabel: 'Move' }))) {
    return { ok: false, cancelled: true, message: 'Cancelled.' };
  }
  try {
    const moved = await deps.call(change.id, offer.name, run.decision?.id ?? '');
    await deps.refresh();
    const state = moved?.state || offer.to;
    deps.notify(`Change moved to “${state}”.`, 'ok');
    return { ok: true, state };
  } catch (e) {
    const message = errorMessage(e);
    return { ok: false, message, refusal: parseRefusal(message) };
  }
}

/** The transition and decision point an assistant call names, or why it cannot be taken. */
export function resolveCall(offers: TransitionOffer[], pickable: DecisionPoint[], args: { transition?: unknown; decision?: unknown }): { offer: TransitionOffer; decision?: DecisionPoint } | string {
  const offer = offers.find((o) => o.name === args.transition);
  if (!offer) return `The transition ${String(args.transition)} does not leave the current state; offered: ${offers.map((o) => o.name).join(', ') || 'none'}.`;
  const id = String(args.decision ?? '');
  const decision = id ? pickable.find((p) => p.id === id) : undefined;
  if (id && !decision) return `The decision point ${id} is not decided or was already used; available: ${pickable.map((p) => p.id).join(', ') || 'none'}.`;
  if (offer.needsDecision && !decision) return `The transition ${offer.name} needs a decided decision point; available: ${pickable.map((p) => p.id).join(', ') || 'none'}.`;
  return { offer, decision };
}

/** The lifecycle of the domain of the namespace, else of any other domain (a built-in one for instance). */
export async function findLifecycle(name: string, ns: string, signal?: AbortSignal): Promise<{ lc: Lifecycle; domain: string } | undefined> {
  const names = (await registry.listDomains(false, signal)).domains?.map((d) => d.name ?? '') ?? [];
  for (const dn of [ns, ...names.filter((n) => n !== ns)].filter(Boolean)) {
    if (!names.includes(dn)) continue;
    const d = (await registry.getDomain(dn, '', signal)).domain;
    const lc = d?.lifecycles?.find((l) => l.name === name);
    if (lc) return { lc, domain: dn };
  }
  return undefined;
}
