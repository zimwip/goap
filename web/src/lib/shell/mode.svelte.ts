// The modes of the front (ADR 0158), switched from the header: what the person is doing decides the tools shown.
// Project mode navigates the project, its open changes and their tasks; change mode works in the working change, the
// only mode meant to write the graph; navigation (the graph and its history) and administration (domains,
// methodologies, projects, organisation) are for administrators. A mode is a choice of the person in this page: it is
// not remembered (a new page starts in change mode when a change is worked in, else in project mode). Each mode keeps
// its workspace: the tool selected and the editors open; switching mode puts the workspace of the one left aside and
// shows the one of the mode entered as it was left (its tool and its view the first time), nothing is closed.
import { untrack } from 'svelte';
import type { IconName } from './Icon.svelte';
import { layout } from './layout.svelte';
import { swapTabs, type TabSet } from './tabs.svelte';
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
  /** the navigation tool shown when the mode is entered */
  tool: string;
  /** a tool filling the editor space shown when the person enters the mode the first time (a dashboard) */
  view?: string;
}

export const MODES: ModeInfo[] = [
  { id: 'project', label: 'Project', icon: 'folder', hint: 'Browse the project, its open changes and their tasks', tool: 'changes', view: 'tasks' },
  { id: 'change', label: 'Change', icon: 'diff', hint: 'Work in the working change: the only mode that modifies the graph', tool: 'changes' },
  { id: 'navigation', label: 'Navigation', icon: 'branch', hint: 'The graph and its history: baselines, diffs and versions', admin: true, tool: 'baselines' },
  { id: 'admin', label: 'Admin', icon: 'settings', hint: 'Domains, methodologies, projects and the organisation', admin: true, tool: 'methodologies' },
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

/** The workspace of a mode put aside: its tools and its editors. */
interface Workspace {
  left: string;
  full: string;
  tabs: TabSet;
}

/** the workspaces of the modes not shown */
const aside = new Map<Mode, Workspace>();

/** Shows mode m in place of the current one: the workspace shown is put aside, the one of m shown as it was left. */
function switchTo(m: Mode): void {
  const from = mode.current;
  if (from === m) return;
  const back = aside.get(m);
  aside.delete(m);
  aside.set(from, { left: layout.left, full: layout.full, tabs: swapTabs(back?.tabs) });
  mode.current = m;
  layout.left = back?.left ?? modeInfo(m).tool;
  if (!toolInMode(layout.left, m)) layout.left = modeInfo(m).tool;
  layout.full = back ? back.full : (modeInfo(m).view ?? '');
}

/** Enters a mode (false when it cannot be entered now), with its workspace as the person left it. */
export function setMode(m: Mode): boolean {
  if (!modeEnabled(m)) return false;
  chosen = true;
  switchTo(m);
  return true;
}

// a page opens on the view of its first mode (the tasks): a tab the address names takes the space back
untrack(() => (layout.full = modeInfo(mode.current).view ?? ''));

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
  // a tool filling the editor space belongs to the mode too: another mode gives the space back to the editors
  $effect(() => {
    const m = mode.current;
    if (layout.full && !toolInMode(layout.full, m)) untrack(() => (layout.full = ''));
  });
  // the left tool shown belongs to the mode: a tool asked for elsewhere (a command, a link) takes the person to a mode
  // that shows it, the current one staying when none may be entered (a page opens on the default tool of the layout)
  $effect(() => {
    const m = mode.current;
    const tool = layout.left;
    if (toolInMode(tool, m)) return;
    untrack(() => {
      const other = chosen ? MODES.find((x) => toolInMode(tool, x.id) && modeEnabled(x.id)) : undefined;
      if (other) {
        // the tool asked for is shown in the workspace of that mode
        switchTo(other.id);
        layout.left = tool;
        layout.full = '';
      } else layout.left = modeInfo(m).tool;
    });
  });
});
