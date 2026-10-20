import { rpc } from './transport';
import type { Empty } from './types/common';
import type { SmartTagJson, TaggedVersionJson } from '../../gen/goap/smarttag/v1/smarttag_pb';

const SMART_TAGS = 'goap.smarttag.v1.SmartTagService';

/** A shared selection of exact node versions (ADR 0143 §7). */
export type SmartTag = SmartTagJson;
/** One exact node version of a smart tag. */
export type TaggedVersion = TaggedVersionJson;

/**
 * The smart tags of the caller (ADR 0143 §7): kept outside the graph, owned by their creator and shared with users to
 * read or to edit. Changing a selection is an edit of the smart tag, never a change of the nodes.
 */
export const smartTags = {
  /** The smart tags the caller owns or that are shared with them; with `nodeId`, those selecting a version of it. */
  list: (nodeId = '', signal?: AbortSignal) =>
    rpc<{ nodeId: string }, { smartTags?: SmartTag[] }>(SMART_TAGS, 'ListSmartTags', { nodeId }, signal),
  get: (id: string, signal?: AbortSignal) => rpc<{ id: string }, { smartTag: SmartTag }>(SMART_TAGS, 'GetSmartTag', { id }, signal),
  create: (name: string, description = '', versions: TaggedVersion[] = []) =>
    rpc<{ name: string; description: string; versions: TaggedVersion[] }, { smartTag: SmartTag }>(SMART_TAGS, 'CreateSmartTag', {
      name,
      description,
      versions,
    }),
  /** Renames, describes and edits the selection (its owner or an editor). */
  update: (id: string, patch: { name?: string; description?: string; add?: TaggedVersion[]; remove?: TaggedVersion[] }) =>
    rpc<object, { smartTag: SmartTag }>(SMART_TAGS, 'UpdateSmartTag', {
      id,
      name: patch.name ?? '',
      ...(patch.description !== undefined ? { description: patch.description, setDescription: true } : {}),
      add: patch.add ?? [],
      remove: patch.remove ?? [],
    }),
  /** Shares with a user to read or to edit; access '' withdraws the share (its owner). */
  share: (id: string, subject: string, access: 'read' | 'edit' | '') =>
    rpc<{ id: string; subject: string; access: string }, { smartTag: SmartTag }>(SMART_TAGS, 'ShareSmartTag', { id, subject, access }),
  remove: (id: string) => rpc<{ id: string }, Empty>(SMART_TAGS, 'DeleteSmartTag', { id }),
};
