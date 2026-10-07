// The goal of a change (ADR 0096): the main goal of its methodology at creation, then only an explicit edit. The
// methodology may have changed since, so a stored goal it no longer declares is shown as unknown, never blocked.
import type { Methodology } from './api';

export interface GoalInfo {
  /** 'loading': the methodology is not read yet; 'unknown': it declares neither a goal nor a process of that name */
  state: 'known' | 'unknown' | 'loading';
  description: string;
}

/** What the methodology says of a goal: a declared goal, or a process (which reaches the goal of its name). */
export function goalInfo(m: Methodology | undefined, goal: string): GoalInfo {
  if (!m) return { state: 'loading', description: '' };
  const g = (m.goals ?? []).find((x) => x.name === goal) ?? (m.processes ?? []).find((x) => x.name === goal);
  return g ? { state: 'known', description: g.description ?? '' } : { state: 'unknown', description: '' };
}
