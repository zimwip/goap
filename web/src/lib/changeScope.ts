// The scope of the change editor (ADR 0032 §6): the flow the user looks at — the main flow, or one option. It is local
// to the editor: looking at an option does not make it the active one (the option the change and its agents work on).
import type { ChangeImpact, Flow } from './api';

/** The main flow of a change, named explicitly (an empty flow means the active option to the graph). */
export const MAIN_SCOPE = 'main';

const HUES = [265, 160, 25, 200, 330, 95, 45, 290];

/** The colour of a scope: the main flow keeps the accent, each option gets its own hue (by opening order). */
export function scopeColor(options: Flow[], scope: string): string {
  if (!scope || scope === MAIN_SCOPE) return 'var(--accent)';
  const i = options.findIndex((o) => o.id === scope);
  return `hsl(${HUES[Math.max(0, i) % HUES.length]} 60% 48%)`;
}

/** The name of a scope. */
export function scopeName(options: Flow[], scope: string): string {
  if (!scope || scope === MAIN_SCOPE) return 'Main flow';
  return options.find((o) => o.id === scope)?.option?.name ?? scope.slice(0, 8);
}

/** Candidate change impacts of each option (declared on it, not replaced), from the whole list of the change. */
export function candidatesByOption(nodes: ChangeImpact[]): Map<string, number> {
  const out = new Map<string, number>();
  for (const n of nodes) if (n.flow && !n.superseded) out.set(n.flow, (out.get(n.flow) ?? 0) + 1);
  return out;
}

/** Whether the scope can be edited: the main flow of an open change, or an option still open. */
export function scopeWritable(options: Flow[], scope: string, closed: boolean): boolean {
  if (closed) return false;
  if (!scope || scope === MAIN_SCOPE) return true;
  return options.find((o) => o.id === scope)?.status === 'open';
}
