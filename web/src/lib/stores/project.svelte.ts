// The active project (ADR 0039): selected before any non-administrative action, shown in the header
// between the live indicator and the notification bell. Switching it reissues the caller's token
// (api.switchProject) so every call from here on carries it — best effort: a deployment with no token
// (AuthMode "none", e.g. goap-dev with GOAP_AUTH_MODE=none) has nothing to reissue, so the selection stays local-display only there.
import { headGraph } from '../graphEdit';
import { errorMessage, nodeTitle, switchProject as reissueToken, type GraphNode } from '../api';
import { PROJECT_UNIT_TYPE } from '../orgTypes';
import { notify } from '../shell/workbench.svelte';
import { refreshIdentity, session } from './session.svelte';

const STORAGE_KEY = 'goap.project';

function storedProject(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? '';
  } catch {
    return '';
  }
}

export interface ProjectOption {
  key: string;
  label: string;
}

export const project = $state({
  /** the selected project's key ('': the root project) */
  current: storedProject(),
  options: [] as ProjectOption[],
  loading: false,
  error: '',
});

export async function refreshProjects(): Promise<void> {
  project.loading = true;
  try {
    const h = await headGraph('organisation');
    project.options = h.nodes
      .filter((n): n is GraphNode & { key: string } => n.type === PROJECT_UNIT_TYPE && !!n.key)
      .map((n) => ({ key: n.key, label: nodeTitle(n) || n.key }))
      .sort((a, b) => a.label.localeCompare(b.label));
    project.error = '';
  } catch (e) {
    project.error = errorMessage(e);
  } finally {
    project.loading = false;
  }
}

/** Switches the active project: persisted locally, and (best effort) reissued into the token. */
export async function selectProject(key: string): Promise<void> {
  project.current = key;
  try {
    localStorage.setItem(STORAGE_KEY, key);
  } catch {
    // storage unavailable: the selection still applies for this session
  }
  try {
    await reissueToken(key);
    await refreshIdentity();
  } catch {
    // no active token to reissue (auth mode "none"): the selection stays local-display only
    notify('Project selection is local to this browser only: no token to carry it to the server.', 'info');
  }
}

// once the identity is known, adopt its project claim if the local selection disagrees (a token reissued
// elsewhere, or the first load) — never overrides an explicit local pick with an empty claim.
let lastSeenSubject = '';
$effect.root(() => {
  $effect(() => {
    const p = session.principal;
    if (!p || p.subject === lastSeenSubject) return;
    lastSeenSubject = p.subject ?? '';
    if (p.project) project.current = p.project;
  });
});
