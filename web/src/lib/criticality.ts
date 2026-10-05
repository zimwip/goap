// Criticality of a change (ADR 0075 §3): C1 low, C2 current, C3 critical, kept in the free-form data of the change; the
// server enforces who may lower it, what each level requires is policy of the organisation.
import type { Change } from './api';

export const LEVELS = ['C1', 'C2', 'C3'] as const;
export type Level = (typeof LEVELS)[number];
/** the level of a change that names none */
export const DEFAULT_LEVEL: Level = 'C2';

export const LEVEL_LABEL: Record<Level, string> = { C1: 'C1 · low', C2: 'C2 · current', C3: 'C3 · critical' };

const isLevel = (v: unknown): v is Level => LEVELS.includes(v as Level);

/** The criticality of a change: its data's, else the platform default. */
export function criticalityOf(change?: Pick<Change, 'data'>): Level {
  const v = (change?.data as Record<string, unknown> | undefined)?.criticality;
  return isLevel(v) ? v : DEFAULT_LEVEL;
}

/** Whether moving from one level to another lowers the criticality. */
export const lowers = (from: Level, to: Level): boolean => LEVELS.indexOf(to) < LEVELS.indexOf(from);

/** The levels the caller may pick: all of them when they may lower it, else the current one and the higher ones. */
export function selectable(current: Level, mayLower: boolean): Level[] {
  return LEVELS.filter((l) => mayLower || !lowers(current, l));
}
