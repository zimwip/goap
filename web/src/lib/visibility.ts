// How far a version of a node is seen (ADR 0161 §4): declared by the domain on a lifecycle state, else on the node
// type, stamped on the version at commit.
import type { Visibility } from './api';

/** The scopes, narrowest first. */
export const VISIBILITIES: Visibility[] = ['personal', 'private', 'project', 'tree', 'public'];

/** What a scope means to the person reading a node. */
export function visibilityLabel(v: string | undefined): string {
  switch (v || 'project') {
    case 'personal':
      return 'Personal: its creator only';
    case 'private':
      return 'Private: the members of its responsible unit';
    case 'tree':
      return 'Tree: the projects around its project';
    case 'public':
      return 'Public: every identified person';
    default:
      return 'Project: those who work on its project';
  }
}
