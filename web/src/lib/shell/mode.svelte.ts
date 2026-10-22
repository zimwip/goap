// The modes of the front (ADR 0158), switched from the header: what the person is doing decides the tools shown.
// Project mode navigates the project, its open changes and their tasks; change mode works in the working change, the
// only mode meant to write the graph; navigation (the graph and its history) and administration (domains,
// methodologies, projects, organisation) are for administrators. A mode is a choice of the person in this page: it is
// not remembered (a new page starts in change mode when a change is worked in, else in project mode).
import { untrack } from 'svelte';
import type { IconName } from './Icon.svelte';
import { layout } from './layout.svelte';
import { can } from '../stores/session.svelte';
import { working } from '../stores/workingChange.svelte';

export type Mode = 'project' | 'change' | 'navigation' | 'admin';

export interface ModeInfo {
  id: Mode;
  label: string;
  icon: IconName;
  /** what the mode is for, in the switch's tooltip */
  hint: string;
  /** administrators only */
  admin?: boolean;
  /** the left tool shown when the mode is entered */
  tool: string;
}

export const MODES: ModeInfo[] = [
  { id: 'project', label: 'Project', icon: 'folder', hint: 'Browse the project, its open changes and their tasks', tool: 'tasks' },
  { id: 'change', label: 'Change', icon: 'diff', hint: 'Work in the working change: the only mode that modifies the graph', tool: 'tasks' },
  { id: 'navigation', label: 'Navigation', icon: 'branch', hint: 'The graph and its history: baselines, diffs and versions', admin: true, tool: 'baselines' },
  { id: 'admin', label: 'Administration', icon: 'settings', hint: 'Domains, methodologies, projects and the organisation', admin: true, tool: 'methodologies' },
];

/**
 * The modes each left tool shows in. A tool not listed shows in every mode (a tool a later version adds is not hidden
 * until it is placed).
 */
export const TOOL_MODES: Record<string, Mode[]> = {
  tasks: ['project', 'change'],
  changes: ['project', 'change'],
  processes: ['project', 'change'],
  graph: ['project', 'change', 'navigation'],
  baselines: ['navigation'],
  stats: ['navigation'],
  methodologies: ['admin'],
  domains: ['admin'],
  triggers: ['admin'],
  peopleOrg: ['admin'],
  adapters: ['admin'],
  access: ['admin'],
};

export const mode = $state({ current: 'project' as Mode });

/** the person picked a mode in this page (until then, the page follows the working change once it is read) */
let chosen = false;

export const modeInfo = (m: Mode): ModeInfo => MODES.find((x) => x.id === m) ?? MODES[0];

/** Whether the person may enter a mode now: change mode needs a working change, the last two an administrator. */
export function modeEnabled(m: Mode): boolean {
  if (m === 'change') return !!working.change;
  if (modeInfo(m).admin) return can.administer;
  return true;
}

/** The modes offered in the header: the administrators' ones only to administrators. */
export const offeredModes = (): ModeInfo[] => MODES.filter((m) => !m.admin || can.administer);

/** Whether a left tool shows in a mode. */
export const toolInMode = (tool: string, m: Mode = mode.current): boolean => TOOL_MODES[tool]?.includes(m) ?? true;

/** Enters a mode (false when it cannot be entered now), showing its tool unless the one shown belongs to it too. */
export function setMode(m: Mode): boolean {
  if (!modeEnabled(m)) return false;
  chosen = true;
  mode.current = m;
  if (!toolInMode(layout.left, m)) layout.left = modeInfo(m).tool;
  return true;
}

$effect.root(() => {
  // the mode follows what the person may do: no working change any more, back to the project; the rights of an
  // administrator gone (another sign-in), back to the project too
  $effect(() => {
    const m = mode.current;
    if (!modeEnabled(m)) untrack(() => setMode('project'));
  });
  // a page opens in change mode when a change is worked in (the working change is read after the page starts)
  $effect(() => {
    if (working.change && !chosen) untrack(() => (mode.current = 'change'));
  });
  // the left tool shown belongs to the mode: a tool asked for elsewhere (a command, a link) takes the person to a mode
  // that shows it, the current one staying when none may be entered (a page opens on the default tool of the layout)
  $effect(() => {
    const m = mode.current;
    const tool = layout.left;
    if (toolInMode(tool, m)) return;
    untrack(() => {
      const other = chosen ? MODES.find((x) => toolInMode(tool, x.id) && modeEnabled(x.id)) : undefined;
      if (other) mode.current = other.id;
      else layout.left = modeInfo(m).tool;
    });
  });
});
