import { describe, expect, it } from 'vitest';
import source from './help/dsl.md?raw';
import { ALGORITHM_USAGES, DSL_FUNCTIONS } from './dsl';
import { fieldNames, groupFunctions, isRelevant, overview, paramAccess, paramNote, signature, splitSections, starter, usageHelp } from './dslHelp';

const sections = splitSections(source);
const section = (prefix: string) => sections.find((s) => s.title.startsWith(prefix));

describe('usageHelp', () => {
  it('describes every usage, with the functions of its ctx', () => {
    for (const { usage } of overview()) {
      const h = usageHelp(usage);
      expect(h.title, usage).not.toBe('');
      expect(h.where, usage).not.toBe('');
      expect(h.functions.length, usage).toBeGreaterThan(0);
    }
    expect(overview().map((o) => o.usage)).toEqual(['action', ...ALGORITHM_USAGES.map((u) => u.usage)]);
  });

  it('gives the action the ctx of script actions and an algorithm the ctx of its usage', () => {
    expect(usageHelp('action').functions).toBe(DSL_FUNCTIONS);
    expect(usageHelp('property_validator').functions.map((f) => f.name)).toContain('value');
    expect(usageHelp('property_validator').functions.map((f) => f.name)).not.toContain('addArtifact');
  });

  it('points every usage at sections of the reference that exist', () => {
    for (const { usage } of overview()) {
      for (const r of usageHelp(usage).reference) expect(section(r), `${usage}: ${r}`).toBeDefined();
    }
  });

  it('has a starter script', () => {
    for (const { usage } of overview()) expect(starter(usage).trim(), usage).not.toBe('');
  });
});

describe('ctx API against docs/dsl.md', () => {
  const documented = (title: string) => new Set([...(section(title)?.body.matchAll(/`ctx\.(\w+)\(/g) ?? [])].map((m) => m[1]));

  it('lists every function the reference documents for script actions', () => {
    const known = new Set(DSL_FUNCTIONS.map((f) => f.name));
    const names = new Set(['Reading', 'Writing', 'Calls'].flatMap((t) => [...documented(t)]));
    expect(names.size).toBeGreaterThan(10);
    expect([...names].filter((n) => !known.has(n))).toEqual([]);
  });

  it('lists every function the reference documents for algorithms', () => {
    const known = new Set(ALGORITHM_USAGES.filter((u) => u.usage !== 'adapter').flatMap((u) => u.functions.map((f) => f.name)));
    expect([...documented('Algorithms')].filter((n) => !known.has(n))).toEqual([]);
  });
});

describe('signature', () => {
  const fn = { name: 'impactNodeCreate', args: ['type', 'key'], returns: '"#nN"', doc: '', group: 'write' as const };
  it('is the camelCase call of the JavaScript ctx', () => {
    expect(signature(fn)).toBe('ctx.impactNodeCreate(type, key)');
  });
  it('names the fields of a shape as they are', () => {
    const shape = { name: 'Node', summary: '', fields: ['id', 'props'] };
    expect(fieldNames(shape)).toEqual(['id', 'props']);
  });
});

describe('groupFunctions', () => {
  it('groups and drops the empty groups', () => {
    const groups = groupFunctions(DSL_FUNCTIONS);
    expect(groups.map((g) => g.group)).toEqual(['read', 'write', 'call']);
    expect(groupFunctions(usageHelp('property_validator').functions).map((g) => g.group)).toEqual(['read', 'call']);
  });
  it('filters on the name, the doc and the return type, ignoring case', () => {
    expect(groupFunctions(DSL_FUNCTIONS, 'IMPACTNODEREVIEW').flatMap((g) => g.fns.map((f) => f.name))).toEqual(['impactNodeReview', 'impactNodeReviewWithReserve']);
    expect(groupFunctions(DSL_FUNCTIONS, 'mcp tool').flatMap((g) => g.fns.map((f) => f.name))).toContain('callTool');
    expect(groupFunctions(DSL_FUNCTIONS, 'no such thing')).toEqual([]);
  });
});

describe('parameters', () => {
  const p = { name: 'pattern', type: 'regex', description: '', required: true, defaultValue: '', values: '' };
  it('says how a script reads one', () => {
    expect(paramAccess(p)).toBe('ctx.param("pattern")');
  });
  it('notes what is required, restricted and defaulted', () => {
    expect(paramNote(p)).toBe('required');
    expect(paramNote({ ...p, required: false, values: 'a, b', defaultValue: 'a' })).toBe('one of a, b · default a');
    expect(paramNote({ ...p, required: false })).toBe('');
  });
});

describe('splitSections', () => {
  it('splits on level 2 headings and keeps the text before the first', () => {
    const s = splitSections('# T\n\nintro\n\n## A\n\na\n\n## B\n\nb\n');
    expect(s.map((x) => x.title)).toEqual(['', 'A', 'B']);
    expect(s[1].body.trim()).toBe('a');
  });
  it('never splits inside a fenced block', () => {
    const s = splitSections('## A\n\n```md\n## not a heading\n```\n\n## B\n');
    expect(s.map((x) => x.title)).toEqual(['A', 'B']);
    expect(s[0].body).toContain('## not a heading');
  });
  it('opens the sections of the usage only', () => {
    const open = (u: Parameters<typeof isRelevant>[1]) => sections.filter((s) => isRelevant(s, u)).map((s) => s.title.split(' ')[0]);
    expect(open('action')).toEqual(['Script', 'Reading', 'Writing', 'Calls', 'Examples']);
    expect(open('node_validator')).toEqual(['Algorithms']);
    expect(open(undefined)).toEqual([]);
  });
});
