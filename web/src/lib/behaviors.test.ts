import { describe, expect, it } from 'vitest';
import { behaviorsLine, byteLength, counter, emptyForm, formOf, scopeSummary, toggled, validateForm } from './behaviors';

describe('behaviours form', () => {
  const ok = { ...emptyForm(), name: 'terse', instruction: 'Be brief.' };

  it('accepts a valid form and names what is wrong otherwise', () => {
    expect(validateForm(ok)).toBe('');
    expect(validateForm({ ...ok, name: 'Bad Name' })).toMatch(/name/i);
    expect(validateForm({ ...ok, instruction: '   ' })).toMatch(/required/);
    expect(validateForm({ ...ok, instruction: 'x'.repeat(11) }, 10)).toMatch(/at most 10/);
  });

  it('counts bytes, not characters', () => {
    expect(byteLength('é')).toBe(2);
    expect(counter('éé', 3)).toEqual({ used: 4, max: 3, over: true });
  });

  it('reads a behaviour into the form with its defaults', () => {
    const f = formOf({ name: 'a', instruction: 'x', enabled: true, position: 'prepend', aliases: ['fast'] });
    expect(f).toMatchObject({ name: 'a', enabled: true, position: 'prepend', aliases: ['fast'], models: [], sources: [], appliesToJson: false });
    expect(formOf({ name: 'b' }).position).toBe('append');
  });

  it('toggles a selector value', () => {
    expect(toggled(['a'], 'b')).toEqual(['a', 'b']);
    expect(toggled(['a', 'b'], 'a')).toEqual(['b']);
  });
});

describe('behaviours summaries', () => {
  it('summarises the scope', () => {
    expect(scopeSummary({})).toBe('every completion');
    expect(scopeSummary({ aliases: ['fast'], sources: ['engine', 'helper'], appliesToJson: true })).toBe('aliases fast · from engine, helper · JSON calls too');
  });

  it('describes what a call got', () => {
    expect(behaviorsLine(undefined, 0)).toBe('');
    expect(behaviorsLine(['terse'], '42')).toBe('terse · about 42 tokens added');
    expect(behaviorsLine(['terse', '!big'], 10)).toBe('terse · about 10 tokens added · dropped by the size cap: big');
    expect(behaviorsLine(['!big'], 0)).toBe('dropped by the size cap: big');
  });
});
