import { describe, expect, it } from 'vitest';
import { buildTree, canMoveFolder, entryOf, follows, holds, subtree, visible } from './projectDirectory';
import type { SmartTag } from './api';

const tag = (id: string, name: string, parent = '', versions: SmartTag['versions'] = []): SmartTag => ({ id, name, parent, versions }) as SmartTag;

describe('projectDirectory', () => {
  const tags = [tag('d', 'Docs'), tag('s', 'Specs', 'd'), tag('a', 'Archive', 'd'), tag('x', 'Drafts', 's'), tag('c', 'Code')];

  it('builds the tree by name, depth by depth', () => {
    const roots = buildTree(tags);
    expect(roots.map((f) => f.tag.name)).toEqual(['Code', 'Docs']);
    expect(roots[1].children.map((f) => f.tag.name)).toEqual(['Archive', 'Specs']);
    expect(roots[1].children[1].children[0].depth).toBe(2);
  });

  it('keeps an orphan and a cycle at the top', () => {
    const roots = buildTree([tag('o', 'Orphan', 'gone'), tag('p', 'P', 'q'), tag('q', 'Q', 'p')]);
    expect(roots.map((f) => f.tag.id).sort()).toContain('o');
    expect(roots.length).toBeGreaterThanOrEqual(2);
  });

  it('lists the open folders only', () => {
    const roots = buildTree(tags);
    expect(visible(roots, () => false).map((f) => f.tag.id)).toEqual(['c', 'd']);
    expect(visible(roots, (id) => id === 'd').map((f) => f.tag.id)).toEqual(['c', 'd', 'a', 's']);
  });

  it('refuses a folder below itself', () => {
    expect([...subtree(tags, 'd')].sort()).toEqual(['a', 'd', 's', 'x']);
    expect(canMoveFolder(tags, 'd', 'x')).toBe(false);
    expect(canMoveFolder(tags, 's', 'd')).toBe(false); // where it is
    expect(canMoveFolder(tags, 's', 'c')).toBe(true);
    expect(canMoveFolder(tags, 's', '')).toBe(true);
    expect(canMoveFolder(tags, 'd', '')).toBe(false);
  });

  it('makes entries following a node or at a version', () => {
    expect(entryOf({ id: 'N', version: 3 }, true)).toEqual({ nodeId: 'N', version: 0 });
    expect(entryOf({ id: 'N', version: 3 }, false)).toEqual({ nodeId: 'N', version: 3 });
    expect(follows({ nodeId: 'N', version: 0 })).toBe(true);
    const t = tag('t', 'T', '', [{ nodeId: 'N', version: 0 }]);
    expect(holds(t, { nodeId: 'N', version: 0 })).toBe(true);
    expect(holds(t, { nodeId: 'N', version: 2 })).toBe(false);
  });
});
