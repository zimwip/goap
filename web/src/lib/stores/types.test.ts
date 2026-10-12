import { describe, expect, it } from 'vitest';
import { TypeCatalog } from './types.svelte';

// Strict attributes: only a type flagged additionalProperties (or one the catalogue does not know) is open.
describe('TypeCatalog.open', () => {
  const cat = new TypeCatalog([{ ref: 'a@Closed' }, { ref: 'a@Free', additionalProperties: true }]);
  it('is closed by default', () => expect(cat.open('a@Closed')).toBe(false));
  it('is open when the type is free-form', () => expect(cat.open('a@Free')).toBe(true));
  it('is open for an unknown type', () => expect(cat.open('a@Nope')).toBe(true));
});

// A link reads from the node looking at it: its label from the source, its inverse label from the target.
describe('TypeCatalog.linkLabel', () => {
  const cat = new TypeCatalog([], [{ ref: 'a@verifies', label: 'verifies', inverseLabel: 'verified by' }, { ref: 'a@traces', label: 'traces to' }]);
  it('reads the label from the source', () => expect(cat.linkLabel('a@verifies', 'out')).toBe('verifies'));
  it('reads the inverse label from the target', () => expect(cat.linkLabel('a@verifies', 'in')).toBe('verified by'));
  it('falls back to the label', () => expect(cat.linkLabel('a@traces', 'in')).toBe('traces to'));
  it('falls back to the name for an unknown link type', () => expect(cat.linkLabel('a@other', 'in')).toBe('other'));
});
