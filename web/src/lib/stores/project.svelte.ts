// The active project (ADR 0039): selected before any non-administrative action, shown in the header
// between the live indicator and the notification bell. Switching it reissues the caller's token
// (api.switchProject) so every call from here on carries it — best effort: a deployment with no token
// (AuthMode "none", e.g. goap-dev with GOAP_AUTH_MODE=none) has nothing to reissue, so the selection stays local-display only there.
import { headGraph } from '../graphEdit';
import { errorMessage, getToken, nodeTitle, switchProject as reissueToken, type GraphNode } from '../api';
import { notify } from '../shell/workbench.svelte';
import { refreshIdentity, session, types as nodeTypes, ns } from './session.svelte';

export interface ProjectOption {
  key: string;
  label: string;
}

export const project = $state({
  /** the selected project's key ('': the root project) */
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
export async function selectProject(key: string): Promise<void> {
  if (!getToken()) {
    project.current = key;
    notify('Project selection is local to this browser only: no token to carry it to the server.', 'info');
    return;
  }
  try {
    await reissueToken(key);
    await refreshIdentity();
  } catch (e) {
    notify(errorMessage(e), 'error');
  }
}

// the project is the identity's claim ('' for the root project): follows every token the identity comes from
$effect.root(() => {
  $effect(() => {
    const p = session.principal;
    if (p) project.current = p.project ?? '';
  });
});
