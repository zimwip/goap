import { describe, expect, it } from 'vitest';
import { addedLine, behaviorsLine, costLine, isEstimated, measuredDate, tokensText, byteLength, counter, emptyForm, formOf, scopeSummary, toggled, validateForm } from './behaviors';

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
    expect(behaviorsLine(['terse'], '42', true)).toBe('terse · about 42 tokens added (estimate)');
    expect(behaviorsLine(['terse', '!big'], 10, true)).toBe('terse · about 10 tokens added (estimate) · dropped by the size cap: big');
    expect(behaviorsLine(['!big'], 0)).toBe('dropped by the size cap: big');
  });
});

describe('behaviour costs', () => {
  it('marks an estimate and a measure', () => {
    expect(tokensText(23, true)).toBe('≈ 23');
    expect(tokensText('23')).toBe('23');
    expect(behaviorsLine(['terse'], 42, false)).toBe('terse · 42 tokens added (measured)');
  });

  it('describes the cost on a model', () => {
    expect(costLine({ behavior: 't', model: 'anthropic/claude-x', tokens: 23, source: 'estimated', measuredAtMs: 0 })).toBe('≈ 23 tokens on claude-x');
    expect(costLine({ behavior: 't', model: 'anthropic/claude-x', tokens: 23, source: 'measured', measuredAtMs: Date.UTC(2026, 9, 7, 12) })).toBe('23 tokens on claude-x, measured 2026-10-07');
    expect(costLine({ behavior: 't', model: 'fake/echo', tokens: 9, source: 'estimated', measuredAtMs: 1000 })).toContain('no usable count');
    expect(isEstimated({ source: undefined })).toBe(true);
    expect(measuredDate(0)).toBe('');
  });

  it('describes the preview', () => {
    expect(addedLine(12, true)).toMatch(/about 12 tokens added \(estimate/);
    expect(addedLine(12, false)).toBe('12 tokens added (measured)');
  });
});
