import { rpc } from './transport';
import type { Empty } from './types/common';
import type { SmartTagJson, TaggedVersionJson } from '../../gen/goap/smarttag/v1/smarttag_pb';

const SMART_TAGS = 'goap.smarttag.v1.SmartTagService';

/** A shared selection of node versions (ADR 0143 §7), a folder of a tree (ADR 0178). */
export type SmartTag = SmartTagJson;
/** An entry of a smart tag: an exact node version, or with version 0 the node itself, followed in the reader's view. */
export type TaggedVersion = TaggedVersionJson;

/**
 * The smart tags of the caller (ADR 0143 §7): kept outside the graph, owned by their creator and shared with users to
 * read or to edit. Changing a selection is an edit of the smart tag, never a change of the nodes.
 */
export const smartTags = {
  /** The smart tags the caller reads (owned, shared, below one of those); with `nodeId`, those selecting it. */
  list: (nodeId = '', signal?: AbortSignal) =>
    rpc<{ nodeId: string }, { smartTags?: SmartTag[] }>(SMART_TAGS, 'ListSmartTags', { nodeId }, signal),
  /** The smart tags of a project, every tree: its directory (ADR 0178). */
  ofProject: (project: string, signal?: AbortSignal) =>
    rpc<{ project: string }, { smartTags?: SmartTag[] }>(SMART_TAGS, 'ListSmartTags', { project }, signal),
  get: (id: string, signal?: AbortSignal) => rpc<{ id: string }, { smartTag: SmartTag }>(SMART_TAGS, 'GetSmartTag', { id }, signal),
  /** A root of the caller (of `project` when named), or with `parent` a sub-tag in its parent's project. */
  create: (name: string, description = '', versions: TaggedVersion[] = [], where: { parent?: string; project?: string } = {}) =>
    rpc<{ name: string; description: string; versions: TaggedVersion[]; parent: string; project: string }, { smartTag: SmartTag }>(SMART_TAGS, 'CreateSmartTag', {
      name,
      description,
      versions,
      parent: where.parent ?? '',
      project: where.project ?? '',
    }),
  /** Renames, describes, edits the selection and moves (`parent`, '' a root) a smart tag: whoever edits its tree. */
  update: (id: string, patch: { name?: string; description?: string; add?: TaggedVersion[]; remove?: TaggedVersion[]; parent?: string }) =>
    rpc<object, { smartTag: SmartTag }>(SMART_TAGS, 'UpdateSmartTag', {
      id,
      name: patch.name ?? '',
      ...(patch.description !== undefined ? { description: patch.description, setDescription: true } : {}),
      add: patch.add ?? [],
      remove: patch.remove ?? [],
      ...(patch.parent !== undefined ? { parent: patch.parent, setParent: true } : {}),
    }),
  /** Shares with a user to read or to edit; access '' withdraws the share (its owner). */
  share: (id: string, subject: string, access: 'read' | 'edit' | '') =>
    rpc<{ id: string; subject: string; access: string }, { smartTag: SmartTag }>(SMART_TAGS, 'ShareSmartTag', { id, subject, access }),
  remove: (id: string) => rpc<{ id: string }, Empty>(SMART_TAGS, 'DeleteSmartTag', { id }),
};
