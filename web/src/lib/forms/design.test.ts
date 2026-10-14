import { describe, expect, it } from 'vitest';
import {
  applyPattern,
  changeWidget,
  designIssues,
  formJSON,
  insertField,
  moveField,
  newField,
  palette,
  parseForm,
  PATTERNS,
  removeField,
  setConstraint,
  updateField,
} from './design';
import type { FormDef } from './types';

const names = (f: FormDef) => (f.fields ?? []).map((x) => x.name);

describe('form designer', () => {
  it('offers every widget of the registry, the common ones first', () => {
    const p = palette().map((w) => w.name);
    expect(p.slice(0, 3)).toEqual(['text', 'textarea', 'number']);
    expect(p).toContain('node');
    expect(p).toContain('link');
  });

  it('adds a field where it is dropped, with the constraints its widget requires', () => {
    let f: FormDef = { fields: [] };
    f = insertField(f, newField(f, 'node'));
    f = insertField(f, newField(f, 'select'), 0);
    f = insertField(f, newField(f, 'link'));
    f = insertField(f, newField(f, 'node'));
    expect(names(f)).toEqual(['select', 'node', 'link', 'node2']);
    expect(f.fields![0].constraints).toEqual({ options: ['option 1', 'option 2'] });
    expect(f.fields![2].constraints).toEqual({ from: 'node' });
    expect(designIssues(f)).toEqual({});
  });

  it('moves, renames and removes, keeping the references', () => {
    let f: FormDef = { fields: [] };
    for (const w of ['node', 'link', 'text']) f = insertField(f, newField(f, w));
    f = moveField(f, 2, 0);
    expect(names(f)).toEqual(['text', 'node', 'link']);
    f = moveField(f, 0, 3);
    expect(names(f)).toEqual(['node', 'link', 'text']);
    f = updateField(f, 0, { name: 'requirement' });
    expect(f.fields![1].constraints?.['from']).toBe('requirement');
    f = removeField(f, 0);
    expect(f.fields![0].constraints).toBeUndefined();
    expect(designIssues(f)['0']).toContain('choose the source: a node field or a fixed key');
  });

  it('keeps only the constraints the widget accepts', () => {
    const f = setConstraint(setConstraint({ name: 'x', widget: 'text' }, 'maxLength', 10), 'pattern', '');
    expect(f.constraints).toEqual({ maxLength: 10 });
    expect(changeWidget(f, 'number').constraints).toBeUndefined();
  });

  it('reports what the server would refuse', () => {
    const is = designIssues({
      fields: [
        { name: '1a', widget: 'text' },
        { name: 'b', widget: 'select' },
        { name: 'b', widget: 'text', constraints: { options: ['x'] } },
        { name: 'c', widget: 'link', constraints: { from: 'b' } },
      ],
    });
    expect(is['0']).toEqual(['the name is a letter, then letters, digits or _']);
    expect(is['1']).toEqual(['options is required']);
    expect(is['2']).toEqual(['another field is named b', 'text has no constraint options']);
    expect(is['3']).toEqual(['b is not a node search field']);
  });

  it('adds a pattern, renaming its fields when taken', () => {
    let f: FormDef = { fields: [{ name: 'node', widget: 'text' }] };
    f = applyPattern(f, PATTERNS[0]);
    expect(names(f)).toEqual(['node', 'node2', 'links', 'why']);
    expect(f.fields![2].constraints?.['from']).toBe('node2');
    expect(f.submit).toBe('Link');
    for (const p of PATTERNS) expect(designIssues(p.form)).toEqual({});
  });

  it('reads and writes the JSON of the action', () => {
    expect(parseForm('')).toEqual({ form: { fields: [] } });
    expect(parseForm('[1]').error).toBeTruthy();
    expect(formJSON({ title: ' ', fields: [] })).toBe('');
    const json = formJSON({ title: 'T', fields: [{ name: 'a', widget: 'text' }] });
    expect(parseForm(json).form).toEqual({ title: 'T', fields: [{ name: 'a', widget: 'text' }] });
  });
});
