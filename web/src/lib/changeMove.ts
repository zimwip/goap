// Moving a change to another project (ADR 0091): what the Move dialog and the assistant's `move_change` tool share.
import { graph } from './api';
import { headGraph } from './graphEdit';
import { applicableMethodologies } from './projectRoles';
import { moveChoices, type MoveChoices } from './changeProject';
import { project, refreshProjects, selectProject } from './stores/project.svelte';
import { ns, rootProject } from './stores/session.svelte';
import { refreshChanges } from './stores/catalog.svelte';
import { notify } from './shell/workbench.svelte';
import type { Change } from './api';

/** The projects a change may move to (the server stays the authority). */
export async function loadMoveOffer(ch: Pick<Change, 'methodology' | 'projectId'>): Promise<MoveChoices> {
  if (!project.options.length) await refreshProjects();
  const head = await headGraph(ns.organisation);
  return moveChoices({
    methodology: ch.methodology,
    current: ch.projectId || rootProject(),
    projects: project.options,
    applicable: (key) => applicableMethodologies(head, key),
  });
}

/** Moves the change; the active project follows it and the catalog is read again. */
export async function moveChangeTo(changeId: string, target: string): Promise<void> {
  await graph.moveChange(changeId, target);
  await selectProject(target);
  await refreshChanges();
  notify(`Change moved to project ${target}.`, 'ok');
}
