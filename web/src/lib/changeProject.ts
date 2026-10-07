// The project of a change (ADR 0091): a change is created in a project and may move to another one while it is open,
// when the methodology of the change applies to both. The server is the authority (the caller's access to both projects
// and the methodology rule are checked there); these helpers only narrow what the interface offers.
import type { Change } from './api';

export interface ProjectChoice {
  key: string;
  label: string;
}

/** A root change still being worked on can move; a sub-change goes with its parent, a landed or abandoned one stays. */
export const movable = (c?: Pick<Change, 'parentId' | 'status'>): boolean => !!c && !c.parentId && (c.status === 'draft' || c.status === 'active');

export interface MoveChoices {
  /** the projects the change may move to */
  choices: ProjectChoice[];
  /** why none is offered ('' when some is) */
  blocked: string;
}

/**
 * The projects a change may move to: every other project whose applicable methodologies (its own and its ancestors',
 * `applicable`) include the methodology of the change; a change with no methodology goes anywhere. When the project the
 * change is in does not list its methodology either, the server refuses any move, so none is offered.
 */
export function moveChoices(opts: {
  methodology?: string;
  current: string;
  projects: ProjectChoice[];
  applicable: (project: string) => string[];
}): MoveChoices {
  const { methodology, current, projects, applicable } = opts;
  const others = projects.filter((p) => p.key !== current);
  if (!methodology) return { choices: others, blocked: others.length ? '' : 'There is no other project.' };
  if (!applicable(current).includes(methodology)) {
    return { choices: [], blocked: `Project ${current} does not list the methodology ${methodology}: the change cannot move.` };
  }
  const choices = others.filter((p) => applicable(p.key).includes(methodology));
  return { choices, blocked: choices.length ? '' : `No other project lists the methodology ${methodology}.` };
}

/** The project a new change starts in: the active project, the claim '' being the root project. */
export const defaultChangeProject = (active: string, root: string, options: ProjectChoice[]): string => {
  const key = active || root;
  return options.some((o) => o.key === key) || !options.length ? key : (options[0]?.key ?? key);
};
