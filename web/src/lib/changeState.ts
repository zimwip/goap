// The current state of a change (ADR 0112): pure helpers over the answer of `GetChangeState`. The engine judges every
// step against the change as it is, then lays over it what the live runs are doing, so the screen shows each process
// once, whatever the number of runs it took; a step that cannot start says which conditions it still needs.
import type { Fact, Need, ProcessState, StepProgress } from './api';

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
