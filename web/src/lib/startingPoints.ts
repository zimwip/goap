// The possible next steps of a change (ADR 0097): pure helpers over the answer of `ListStartingPoints`. The engine says
// which steps (or steps of a method) are possible now towards the goal of the change; the screen only presents them and
// starts one with the launch the engine gave. Nothing here names an action.
import type { StartingPoint, StartingPointsResponse, StartProcessRequest } from './api';

/** The panel is shown for a change that can still take a process and has a methodology and a goal. */
export function showStartingPoints(c: { status?: string; methodology?: string; goal?: string } | undefined): boolean {
  if (!c?.methodology || !c.goal) return false;
  return c.status === 'draft' || c.status === 'active';
}

/** The call that starts exactly one point: the agent plans towards the goal of the step and the scheduler sequences its actions. */
export function startRequest(p: StartingPoint): StartProcessRequest {
  return {
    methodology: p.launch.methodology,
    agent: p.launch.agent,
    goal: p.launch.goal,
    changeId: p.launch.changeId,
    intent: `Carry out the step ${p.id}`,
  };
}

/** The point can be started now by the caller: allowed, and nothing carries it out yet. */
export const startable = (p: StartingPoint): boolean => !!p.mayRun && !p.running;

/** The ids of the points that can be started now. */
export const startableIds = (r: StartingPointsResponse | undefined): string[] => (r?.points ?? []).filter(startable).map((p) => p.id);

/** Finds a point by id. */
export const pointById = (r: StartingPointsResponse | undefined, id: unknown): StartingPoint | undefined => (r?.points ?? []).find((p) => p.id === id);

/** What makes the point possible: the entry conditions that hold. */
export function whyText(p: StartingPoint): string {
  return p.why?.length ? p.why.join(', ') : 'no precondition';
}

/** Who may do it: the roles the agent needs when the caller lacks them, else the responsible role. */
export function rolesText(p: StartingPoint): string {
  if (!p.mayRun && p.needRoles?.length) return `needs the role ${p.needRoles.join(' or ')}`;
  return p.responsible ? `role ${p.responsible}` : 'any member';
}

/** How the point reads: the method that applies when the step names a capability. */
export function kindText(p: StartingPoint): string {
  if (p.kind === 'method') return p.parent ? `step of the method ${p.method ?? ''}` : `method ${p.method ?? ''} (${p.capability ?? ''})`;
  return 'step';
}

/** "N more steps wait for: a, b" for the steps that are not possible yet (never proposed); '' when none. */
export function waitingLine(r: StartingPointsResponse | undefined): string {
  const n = r?.blockedCount ?? 0;
  if (!n) return '';
  const names = [...new Set((r?.blocked ?? []).flatMap((b) => b.missing ?? []))].slice(0, 5);
  return `${n} more step${n === 1 ? '' : 's'} wait${n === 1 ? 's' : ''}${names.length ? ' for: ' + names.join(', ') : ''}`;
}

/** The text of the confirmation before starting a point. */
export function confirmText(p: StartingPoint, goal: string): string {
  const what = p.kind === 'method' && p.method ? `the method ${p.method} for “${p.name}”` : `the step “${p.name}” (${p.id})`;
  return `Start ${what}? The scheduler plans and sequences its actions on this change towards ${goal ? `the goal ${goal}` : 'its goal'}; it produces ${p.produces?.join(', ') || 'what the step states'}.`;
}

/** Why there is nothing to start: the reason of the engine, else a default. */
export function emptyText(r: StartingPointsResponse | undefined): string {
  return r?.reason || 'No step is possible now.';
}
