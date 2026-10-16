// The possible next steps of a change (ADR 0097): pure helpers over the answer of `ListStartingPoints`. The engine says
// which steps (or steps of a method) are possible now towards the goal of the change; the screen only presents them and
// starts one with the launch the engine gave. Nothing here names an action.
// What a step is (name, description, guidance, exit, roles) comes from the view of the methodology version
// (`MethodologyView`), what is possible now from the engine (`StartingPointsBinding`): `composePoints` puts the two
// together by the path of the step, as lib/startingpoint does on the server (ADR 0126 step 5).
import type { MethodologyView, PossibleStep, PossibleStepList, StartingPointsBinding, StartProcessRequest } from './api';

type ViewStep = NonNullable<NonNullable<MethodologyView['processes']>[number]['steps']>[number];
type ViewActivity = NonNullable<MethodologyView['methods']>[number];

/** "name" / "!name" for a set of conditions, sorted. */
const conditionList = (m: Record<string, boolean> | undefined): string[] =>
  Object.entries(m ?? {})
    .map(([k, v]) => (v ? k : `!${k}`))
    .sort();

/** Reads the points the engine bound in the view of their methodology version (none: their ids and bindings only). */
export function composePoints(view: MethodologyView | undefined, b: StartingPointsBinding | undefined): PossibleStepList {
  const steps = new Map<string, ViewStep>();
  const methods = new Map<string, ViewActivity>();
  const index = (list: ViewStep[] | undefined) => {
    for (const s of list ?? []) {
      if (s.path) steps.set(s.path, s);
      index(s.steps);
    }
  };
  for (const a of view?.processes ?? []) index(a.steps);
  for (const a of view?.methods ?? []) {
    if (a.name) methods.set(a.name, a);
    index(a.steps);
  }
  const points = (b?.points ?? []).map((p): PossibleStep => {
    const id = p.id ?? '';
    const s = steps.get(id);
    const me = p.method ? methods.get(p.method) : undefined;
    // the roles of the method win over those of the step (ADR 0035 §2)
    const roles = me?.roles ?? s?.roles;
    const capability = p.kind === 'method' && !p.parent ? s?.capability : me?.for;
    return {
      id,
      kind: p.kind === 'method' ? 'method' : 'step',
      process: s?.owner ?? '',
      parent: p.parent || undefined,
      name: s?.name ?? id,
      method: p.method || undefined,
      capability: capability || undefined,
      description: s?.description || undefined,
      guidance: s?.guidance || undefined,
      why: p.why,
      produces: s ? conditionList(s.exit) : undefined,
      responsible: roles?.responsible || undefined,
      accountable: roles?.accountable || undefined,
      launch: { methodology: p.launch?.methodology ?? '', agent: p.launch?.agent ?? '', goal: p.launch?.goal ?? '', changeId: p.launch?.changeId ?? '' },
      mayRun: p.mayRun,
      needRoles: p.needRoles,
      running: p.running,
    };
  });
  return {
    methodology: b?.methodology,
    version: b?.version,
    goal: b?.goal,
    reason: b?.reason,
    points,
    blocked: (b?.blocked ?? []).map((x) => ({ id: x.id ?? '', name: steps.get(x.id ?? '')?.name ?? x.id ?? '', missing: x.missing })),
    blockedCount: b?.blockedCount,
  };
}

/** The view to read a binding in: the methodology version it names ('' when there is nothing to read). */
export const viewRef = (b: StartingPointsBinding | undefined): string =>
  b?.methodology && (b.points?.length || b.blocked?.length) ? `${b.methodology}@${b.version ?? ''}` : '';

/**
 * The panel is shown for a change that can still take a process. A change with no goal of its own is pulled by the main
 * goal of its methodology (the server); one with no methodology offers to pick one of the project's.
 */
export function showStartingPoints(c: { status?: string; methodology?: string; goal?: string } | undefined): boolean {
  return c?.status === 'draft' || c?.status === 'active';
}

/** The call that starts exactly one point: the agent plans towards the goal of the step and the scheduler sequences its actions. */
export function startRequest(p: PossibleStep): StartProcessRequest {
  return {
    methodology: p.launch.methodology,
    agent: p.launch.agent,
    goal: p.launch.goal,
    changeId: p.launch.changeId,
    intent: `Carry out the step ${p.id}`,
  };
}

/** The point can be started now by the caller: allowed, and nothing carries it out yet. */
export const startable = (p: PossibleStep): boolean => !!p.mayRun && !p.running;

/** The ids of the points that can be started now. */
export const startableIds = (r: PossibleStepList | undefined): string[] => (r?.points ?? []).filter(startable).map((p) => p.id);

/** Finds a point by id. */
export const pointById = (r: PossibleStepList | undefined, id: unknown): PossibleStep | undefined => (r?.points ?? []).find((p) => p.id === id);

/** What makes the point possible: the entry conditions that hold. */
export function whyText(p: PossibleStep): string {
  return p.why?.length ? p.why.join(', ') : 'no precondition';
}

/** Who may do it: the roles the agent needs when the caller lacks them, else the responsible role. */
export function rolesText(p: PossibleStep): string {
  if (!p.mayRun && p.needRoles?.length) return `needs the role ${p.needRoles.join(' or ')}`;
  return p.responsible ? `role ${p.responsible}` : 'any member';
}

/** How the point reads: the method that applies when the step names a capability. */
export function kindText(p: PossibleStep): string {
  if (p.kind === 'method') return p.parent ? `step of the method ${p.method ?? ''}` : `method ${p.method ?? ''} (${p.capability ?? ''})`;
  return 'step';
}

/** "N more steps wait for: a, b" for the steps that are not possible yet (never proposed); '' when none. */
export function waitingLine(r: PossibleStepList | undefined): string {
  const n = r?.blockedCount ?? 0;
  if (!n) return '';
  const names = [...new Set((r?.blocked ?? []).flatMap((b) => b.missing ?? []))].slice(0, 5);
  return `${n} more step${n === 1 ? '' : 's'} wait${n === 1 ? 's' : ''}${names.length ? ' for: ' + names.join(', ') : ''}`;
}

/** The text of the confirmation before starting a point. */
export function confirmText(p: PossibleStep, goal: string): string {
  const what = p.kind === 'method' && p.method ? `the method ${p.method} for “${p.name}”` : `the step “${p.name}” (${p.id})`;
  return `Start ${what}? The scheduler plans and sequences its actions on this change towards ${goal ? `the goal ${goal}` : 'its goal'}; it produces ${p.produces?.join(', ') || 'what the step states'}.`;
}

/** Why there is nothing to start: the reason of the engine, else a default. */
export function emptyText(r: PossibleStepList | undefined): string {
  return r?.reason || 'No step is possible now.';
}
