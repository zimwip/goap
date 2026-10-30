// The directory of a project (ADR 0178): the trees of its smart tags, laid out as folders. A folder holds entries, each
// a node at an exact version or the node itself (version 0, followed in the reader's view); moving a folder or an
// entry is an edit of smart tags, never a change of the graph.
import type { SmartTag, TaggedVersion } from './api';

/** A folder of the directory: a smart tag with the folders below it. */
export interface Folder {
  tag: SmartTag;
  children: Folder[];
  /** 0 for a top-level folder */
  depth: number;
}

const byName = (a: Folder, b: Folder) => (a.tag.name ?? '').localeCompare(b.tag.name ?? '') || (a.tag.id ?? '').localeCompare(b.tag.id ?? '');

/** The folders of a project's tags: the roots (and the tags whose parent is not listed) at the top, by name. */
export function buildTree(tags: SmartTag[]): Folder[] {
  const byId = new Map<string, Folder>();
  for (const t of tags) if (t.id) byId.set(t.id, { tag: t, children: [], depth: 0 });
  const roots: Folder[] = [];
  for (const f of byId.values()) {
    const p = f.tag.parent ? byId.get(f.tag.parent) : undefined;
    if (p && !ancestorsOf(byId, p).includes(f)) p.children.push(f);
    else roots.push(f);
  }
  const place = (fs: Folder[], depth: number) => {
    fs.sort(byName);
    for (const f of fs) {
      f.depth = depth;
      place(f.children, depth + 1);
    }
  };
  place(roots, 0);
  return roots;
}

function ancestorsOf(byId: Map<string, Folder>, f: Folder): Folder[] {
  const out: Folder[] = [f];
  let p = f.tag.parent ? byId.get(f.tag.parent) : undefined;
  while (p && !out.includes(p)) {
    out.push(p);
    p = p.tag.parent ? byId.get(p.tag.parent) : undefined;
  }
  return out;
}

/** The folders in the order a tree shows them, the closed ones' children left out. */
export function visible(roots: Folder[], open: (id: string) => boolean): Folder[] {
  const out: Folder[] = [];
  const walk = (fs: Folder[]) => {
    for (const f of fs) {
      out.push(f);
      if (open(f.tag.id ?? '')) walk(f.children);
    }
  };
  walk(roots);
  return out;
}

/** The ids of a folder and every folder below it. */
export function subtree(tags: SmartTag[], id: string): Set<string> {
  const out = new Set([id]);
  let grew = true;
  while (grew) {
    grew = false;
    for (const t of tags) {
      if (t.id && t.parent && out.has(t.parent) && !out.has(t.id)) {
        out.add(t.id);
        grew = true;
      }
    }
  }
  return out;
}

/** Whether folder `id` may be dropped on `parent` ('' : the top level): not on itself, nor below itself, nor where it is. */
export function canMoveFolder(tags: SmartTag[], id: string, parent: string): boolean {
  const t = tags.find((x) => x.id === id);
  if (!t || (t.parent ?? '') === parent) return false;
  return parent === '' || !subtree(tags, id).has(parent);
}

/** The entry a dropped node becomes: following the node (version 0), or at the version it was shown at. */
export const entryOf = (node: { id: string; version: number }, follow: boolean): TaggedVersion => ({ nodeId: node.id, version: follow ? 0 : node.version });

/** Whether a folder holds that entry already. */
export const holds = (tag: SmartTag, e: TaggedVersion): boolean => (tag.versions ?? []).some((v) => v.nodeId === e.nodeId && (v.version ?? 0) === (e.version ?? 0));

/** A key of an entry, unique in its folder. */
export const entryKey = (e: TaggedVersion): string => `${e.nodeId}@${e.version ?? 0}`;

/** Whether an entry follows its node (version 0) rather than naming one version. */
export const follows = (e: TaggedVersion): boolean => !(e.version ?? 0);
