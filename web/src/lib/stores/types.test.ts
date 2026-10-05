import { describe, expect, it } from 'vitest';
import { TypeCatalog } from './types.svelte';

// Strict attributes: only a type flagged additionalProperties (or one the catalogue does not know) is open.
describe('TypeCatalog.open', () => {
  const cat = new TypeCatalog([{ ref: 'a@Closed' }, { ref: 'a@Free', additionalProperties: true }]);
  it('is closed by default', () => expect(cat.open('a@Closed')).toBe(false));
  it('is open when the type is free-form', () => expect(cat.open('a@Free')).toBe(true));
  it('is open for an unknown type', () => expect(cat.open('a@Nope')).toBe(true));
});
