// The active project (ADR 0039): selected before any non-administrative action, shown in the header
// between the live indicator and the notification bell. None is selected at first: the user (or the assistant, through
// select_project) picks the project they work in; the root project is a project like the others, picked by its key. Switching it reissues the caller's token
// (api.switchProject) so every call from here on carries it — best effort: a deployment with no token
// (AuthMode "none", e.g. the local stack with GOAP_AUTH_MODE=none) has nothing to reissue, so the selection stays local-display only there.
import { headGraph } from '../graphEdit';
import { errorMessage, getToken, nodeTitle, switchProject as reissueToken, type GraphNode } from '../api';
import { applicableMethodologies } from '../projectRoles';
import { notify } from '../shell/workbench.svelte';
import { refreshIdentity, session, types as nodeTypes, ns, rootProject } from './session.svelte';
import { refKey } from '../lnk';

export interface ProjectOption {
  key: string;
  label: string;
}

export const project = $state({
  /** the selected project's key ('': none selected yet) */
  current: '',
  options: [] as ProjectOption[],
  loading: false,
  error: '',
});

export async function refreshProjects(): Promise<void> {
  project.loading = true;
  try {
    const h = await headGraph(ns.organisation);
    project.options = h.nodes
      .filter((n): n is GraphNode & { key: string } => n.type === nodeTypes.projectUnit && !!n.key)
      .map((n) => ({ key: n.key, label: nodeTitle(n) || n.key }))
      .sort((a, b) => a.label.localeCompare(b.label));
    project.error = '';
  } catch (e) {
    project.error = errorMessage(e);
  } finally {
    project.loading = false;
  }
}

/**
 * Switches the active project by reissuing the token, which carries it (the claim is the only copy: nothing
 * is kept in the browser). Without a token (auth mode "none") there is nothing to reissue and the selection
 * only changes the display; any other failure is the caller's to see (a refused token ends the session).
 */
export async function selectProject(key: string): Promise<boolean> {
  if (!getToken()) {
    project.current = key;
    notify('Project selection is local to this browser only: no token to carry it to the server.', 'info');
    return true;
  }
  try {
    await reissueToken(key);
    project.current = key;
    await refreshIdentity();
    return true;
  } catch (e) {
    notify(errorMessage(e), 'error');
    return false;
  }
}

/**
 * The methodologies attached to a project (the active one unless named), its own and its ancestors' (ADR 0039);
 * undefined until read, none outside any project. Loaded on demand by `loadApplicable`, which the assistant calls when
 * it opens and on a project switch.
 */
export const applicable = $state({ names: undefined as string[] | undefined });

export async function loadApplicable(of?: string): Promise<void> {
  const key = of || project.current;
  if (!key) {
    applicable.names = [];
    return;
  }
  try {
    const h = await headGraph(ns.organisation);
    if (key === (of || project.current)) applicable.names = applicableMethodologies(h, key);
  } catch {
    applicable.names = undefined;
  }
}

/** Whether a change belongs to the active project; with none selected, every change does (a change with no project
 * is the root project's). */
export const inActiveProject = (projectId?: string): boolean => !project.current || (refKey(projectId) || rootProject()) === project.current;

// the project is the identity's claim ('' for none): follows every token the identity comes from
$effect.root(() => {
  $effect(() => {
    const p = session.principal;
    if (p) project.current = p.project ?? '';
  });
});
