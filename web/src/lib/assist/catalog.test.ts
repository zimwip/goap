import { describe, expect, it } from 'vitest';
import { descriptor, fieldGuidance, FIELD_GUIDANCE, FIELD_GUIDANCE_DEFAULT, isToolName, TOOLS, type ToolName } from './catalog';
import { MAX_DESCRIPTION_BYTES, MAX_GUIDANCE_BYTES, validateArgs, validateDescriptor } from './schema';

const bytes = (s: string) => new TextEncoder().encode(s).length;

describe('tool catalog', () => {
  const names = Object.keys(TOOLS) as ToolName[];

  it('gives every tool a one-line guidance and a description within the server caps', () => {
    for (const n of names) {
      const t = TOOLS[n];
      expect(t.guidance.trim(), `${n} has no guidance`).not.toBe('');
      expect(t.guidance, `${n} guidance is one line`).not.toContain('\n');
      expect(bytes(t.guidance), `${n} guidance`).toBeLessThanOrEqual(MAX_GUIDANCE_BYTES);
      expect(bytes(t.description), `${n} description`).toBeLessThanOrEqual(MAX_DESCRIPTION_BYTES);
    }
  });

  it('only holds descriptors the server accepts', () => {
    for (const n of names) expect(validateDescriptor(descriptor(n)), n).toBe('');
  });

  it('knows its names, and the model-facing guidance travels in the descriptor', () => {
    expect(isToolName('review_impact')).toBe(true);
    expect(isToolName('toString')).toBe(false);
    expect(descriptor('review_impact').guidance).toBe(TOOLS.review_impact.guidance);
    // a descriptor is a copy: adapting one never changes the catalog
    descriptor('select_impact').args!.properties!.impactId.description = 'changed';
    expect(TOOLS.select_impact.args.properties.impactId.description).toBe('the impact id');
  });

  it('keeps the writes that change data as writes, and the navigation as effects', () => {
    for (const n of ['review_impact', 'rename_change', 'update_intent', 'move_change', 'transition_change', 'start_step', 'set_field', 'create'] as const) expect(TOOLS[n].level, n).toBe('write');
    for (const n of ['select_impact', 'open_impact', 'filter_impacts', 'set_title', 'set_intent', 'set_project', 'set_methodology'] as const) expect(TOOLS[n].level, n).toBe('effect');
  });
});

describe('field guidance', () => {
  it('has a text within the cap for every catalog field', () => {
    for (const [id, g] of Object.entries(FIELD_GUIDANCE)) {
      expect(g.trim(), id).not.toBe('');
      expect(bytes(g), id).toBeLessThanOrEqual(MAX_GUIDANCE_BYTES);
    }
  });

  it('is never empty: its own, the tooltip, the catalog (by id or its prefix), else the default', () => {
    expect(fieldGuidance({ id: 'x', guidance: 'own', description: 'tip' })).toBe('own');
    expect(fieldGuidance({ id: 'x', description: 'tip' })).toBe('tip');
    expect(fieldGuidance({ id: 'review_comment' })).toBe(FIELD_GUIDANCE.review_comment);
    expect(fieldGuidance({ id: 'review_entry_comment:REV-1:I2' })).toBe(FIELD_GUIDANCE.review_entry_comment);
    expect(fieldGuidance({ id: 'whatever' })).toBe(FIELD_GUIDANCE_DEFAULT);
  });

  it('covers the fields the views register by id', () => {
    for (const id of ['title', 'intent', 'methodology', 'review_comment', 'review_global_comment', 'review_entry_comment']) expect(FIELD_GUIDANCE[id], id).toBeTruthy();
  });

  it('tells a reviewer to name what was checked', () => {
    expect(FIELD_GUIDANCE.review_comment).toMatch(/what you checked/);
  });
});

describe('argument validation', () => {
  const schema = descriptor('review_impact').args;
  it('accepts what fits the schema', () => {
    expect(validateArgs(schema, { impactId: 'I1', outcome: 'accept', comment: 'ok' })).toBe('');
  });
  it('refuses unknown, missing, mistyped and out-of-enum arguments', () => {
    expect(validateArgs(schema, { impactId: 'I1', outcome: 'accept', comment: 'ok', x: 1 })).toMatch(/unknown argument x/);
    expect(validateArgs(schema, { impactId: 'I1', outcome: 'accept' })).toMatch(/missing argument comment/);
    expect(validateArgs(schema, { impactId: 1, outcome: 'accept', comment: 'c' })).toMatch(/must be a string/);
    expect(validateArgs(schema, { impactId: 'I1', outcome: 'maybe', comment: 'c' })).toMatch(/one of accept, reject/);
    expect(validateArgs(schema, 'text')).toMatch(/object/);
    expect(validateArgs(schema, { impactId: 'I1', outcome: 'accept', comment: 'é'.repeat(1500) })).toMatch(/longer than/);
  });
  it('checks numbers, booleans and arrays', () => {
    const s = { properties: { n: { type: 'number' as const }, b: { type: 'boolean' as const }, l: { type: 'array' as const, items: { type: 'string' as const } } } };
    expect(validateArgs(s, { n: 1, b: true, l: ['a'] })).toBe('');
    expect(validateArgs(s, { n: NaN })).toMatch(/number/);
    expect(validateArgs(s, { b: 'yes' })).toMatch(/boolean/);
    expect(validateArgs(s, { l: ['a', 2] })).toMatch(/string/);
  });
  it('refuses descriptors the server would', () => {
    const base = descriptor('rename_change');
    expect(validateDescriptor({ ...base, name: 'Bad Name' })).not.toBe('');
    expect(validateDescriptor({ ...base, guidance: 'x'.repeat(201) })).not.toBe('');
    expect(validateDescriptor({ ...base, args: { properties: { a: { type: 'enum', enum: [] } } } })).not.toBe('');
    expect(validateDescriptor({ ...base, args: { properties: { a: { type: 'array', items: { type: 'array' } } } } })).not.toBe('');
  });
});
