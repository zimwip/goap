// The current state of a change (ADR 0112): pure helpers over the answer of `GetChangeState`. The engine judges every
// step against the change as it is, then lays over it what the live runs are doing, so the screen shows each process
// once, whatever the number of runs it took; a step that cannot start says which conditions it still needs.
import type { Fact, MethodologyView, Need, NeedBinding, ProcessState, StepBinding, StepProgress } from './api';

/** A need as a person reads it: what it means (else its name), and the value it needs when that is "false". */
export function needText(n: Need): string {
  const name = n.condition ?? '';
  const what = n.description?.trim() || name.replace(/_/g, ' ');
  return n.want === false ? `not: ${what}` : what;
}

/** A need with what is to be reached first: the conditions it cannot hold without, and the actions producing them
 * ("the description, as it reads now, is rated acceptable, first: the description has been rated (by rate)"). */
export function needDetail(n: Need): string {
  const miss = missingFact(n);
  if (miss) return `${needText(n)}, missing: ${miss.text}${miss.producedBy?.length ? ` (by ${miss.producedBy.join(', ')})` : ''}`;
  const first = (n.requires ?? []).map((r) => (r.producedBy?.length ? `${needText(r)} (by ${r.producedBy.join(', ')})` : needText(r)));
  return first.length ? `${needText(n)}, first: ${first.join('; ')}` : needText(n);
}

/** The first term of the expression of a need that does not hold, with the list it is looked for in. */
export function missingFact(n: Need): Fact | undefined {
  for (const f of n.facts ?? []) {
    for (const p of f.parts ?? []) if (p.missing) return { ...p, text: `${f.over}: ${p.text}` };
    if (f.missing) return f;
  }
  return undefined;
}

/** The facts of a need as lines to show: each term with whether it holds, an exists term part by part. */
export function factLines(n: Need): { text: string; holds: boolean; missing: boolean; depth: number }[] {
  const out: { text: string; holds: boolean; missing: boolean; depth: number }[] = [];
  for (const f of n.facts ?? []) {
    if (f.parts?.length) {
      out.push({ text: `in ${f.over}`, holds: !!f.holds, missing: false, depth: 0 });
      for (const p of f.parts) out.push({ text: p.text ?? '', holds: !!p.holds, missing: !!p.missing, depth: 1 });
    } else out.push({ text: f.text ?? '', holds: !!f.holds, missing: !!f.missing, depth: 0 });
  }
  return out;
}

/** The needs of a step: the engine's, else the names it misses. */
export function needsOf(s: StepProgress): Need[] {
  if (s.needs?.length) return s.needs;
  return (s.missing ?? []).map((m) => (m.startsWith('!') ? { condition: m.slice(1), want: false } : { condition: m, want: true }));
}

/** How many runs a process took, as a short text ("" for none). */
export function runsText(p: ProcessState): string {
  const n = p.runs ?? 0;
  if (!n) return 'not run yet';
  return n === 1 ? '1 run' : `${n} runs`;
}

/** The steps of a process that wait or cannot start, with what they need: the blockers to show first. */
export function blockers(steps: StepProgress[] | undefined): StepProgress[] {
  const out: StepProgress[] = [];
  for (const s of steps ?? []) {
    if (s.steps?.length) {
      out.push(...blockers(s.steps));
      continue;
    }
    if ((s.state === 'todo' || s.state === 'blocked' || (s.state === 'waiting' && s.waiting === 'condition')) && needsOf(s).length) out.push(s);
  }
  return out;
}

/** Orders the processes for the screen: those at work or waiting first, then the ones to do, then the done ones. */
export function ordered(ps: ProcessState[] | undefined): ProcessState[] {
  const rank: Record<string, number> = { waiting: 0, active: 1, blocked: 2, ready: 3, todo: 4, done: 5, skipped: 6 };
  return [...(ps ?? [])].sort((a, b) => (rank[a.state ?? ''] ?? 9) - (rank[b.state ?? ''] ?? 9));
}

// --- the compiled view bound to the change (ADR 0126 step 4) ---------------------------------------------------------
// The registry gives what the steps are (`MethodologyView`, compiled once per version), the engine where they stand on
// the change (`ProcessState.steps`, by path): the screen draws the first and lays the second over it.

type ViewActivity = NonNullable<MethodologyView['processes']>[number];
type ViewStep = NonNullable<ViewActivity['steps']>[number];

/** A missing condition as the screen reads it: the binding of the engine with what the view says of the condition. */
export function needOf(view: MethodologyView | undefined, b: NeedBinding): Need {
  const c = view?.conditions?.[b.condition ?? ''];
  return {
    condition: b.condition,
    want: b.want ?? false,
    description: c?.description,
    producedBy: b.want ? c?.producedBy : undefined,
    requires: (b.requires ?? []).map((r) => ({ condition: r, want: true, description: view?.conditions?.[r]?.description, producedBy: view?.conditions?.[r]?.producedBy })),
    facts: b.facts as Fact[] | undefined,
  };
}

/** A step of the view with where it stands on the change (none: not judged, drawn as to do). */
export function stepOf(view: MethodologyView | undefined, s: ViewStep, b: StepBinding | undefined): StepProgress {
  const byPath = new Map((b?.steps ?? []).map((c) => [c.path ?? '', c]));
  return {
    path: s.path,
    name: s.name,
    description: s.description,
    method: s.method,
    target: s.target,
    guidance: s.guidance,
    checklist: s.checklist,
    references: s.references,
    roles: s.roles,
    state: b?.state ?? 'todo',
    missing: b?.missing,
    needs: (b?.needs ?? []).map((n) => needOf(view, n)),
    runs: b?.runs,
    childProcessIds: b?.childProcessIds,
    lanes: b?.lanes,
    waiting: b?.waiting,
    permission: b?.permission,
    chosen: b?.chosen,
    steps: s.steps?.length ? s.steps.map((c) => stepOf(view, c, byPath.get(c.path ?? ''))) : undefined,
  };
}

/** A process of the change: its activity in the view, its steps drawn from the view with the binding laid over them. */
export function composeProcess(view: MethodologyView | undefined, p: ProcessState): { activity?: ViewActivity; steps: StepProgress[] } {
  const activity = view?.processes?.find((a) => a.name === p.process);
  if (!activity) return { steps: [] };
  const byPath = new Map((p.steps ?? []).map((s) => [s.path ?? '', s]));
  return { activity, steps: (activity.steps ?? []).map((s) => stepOf(view, s, byPath.get(s.path ?? ''))) };
}

/** The state of each step of a process by path, for the flow (its nodes are the steps of the view). */
export function statesByPath(steps: StepProgress[] | undefined, out: Record<string, string> = {}): Record<string, string> {
  for (const s of steps ?? []) {
    if (s.path) out[s.path] = s.state ?? '';
    statesByPath(s.steps, out);
  }
  return out;
}
