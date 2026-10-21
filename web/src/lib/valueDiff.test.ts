import { describe, expect, it } from 'vitest';
import { counts, defaultDiff, diffSeq, diffSides, foldRows, isFold, isInline, jsonText, lineDiff, splitRows, wordDiff } from './valueDiff';
import { formatCode } from './codeText';

/** applies an edit script: what it keeps and adds must give b, what it keeps and removes a */
function check(a: string[], b: string[]) {
  const s = diffSeq(a, b);
  expect(s.filter((x) => x.op !== 'add').map((x) => a[x.a!])).toEqual(a);
  expect(s.filter((x) => x.op !== 'del').map((x) => b[x.b!])).toEqual(b);
  return s.filter((x) => x.op !== 'same').length;
}

describe('diffSeq', () => {
  it('finds the shortest edit script', () => {
    expect(check([], [])).toBe(0);
    expect(check(['a'], [])).toBe(1);
    expect(check([], ['a', 'b'])).toBe(2);
    expect(check('abcabba'.split(''), 'cbabac'.split(''))).toBe(5);
    expect(check('kitten'.split(''), 'sitting'.split(''))).toBe(5);
    expect(check('abc'.split(''), 'abc'.split(''))).toBe(0);
  });
  it('holds on random sequences', () => {
    let seed = 7;
    const rnd = () => ((seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648);
    for (let i = 0; i < 200; i++) {
      const gen = () => Array.from({ length: Math.floor(rnd() * 12) }, () => 'abc'[Math.floor(rnd() * 3)]);
      check(gen(), gen());
    }
  });
});

describe('wordDiff', () => {
  it('marks the words changed', () => {
    expect(wordDiff('the quick fox', 'the slow fox')).toEqual([
      { op: 'same', text: 'the ' },
      { op: 'del', text: 'quick' },
      { op: 'add', text: 'slow' },
      { op: 'same', text: ' fox' },
    ]);
  });
});

describe('lineDiff', () => {
  it('numbers the lines and pairs the edited ones', () => {
    const rows = lineDiff('a\nb x\nc', 'a\nb y\nc\nd');
    expect(rows.map((r) => `${r.op}:${r.old ?? ''}:${r.new ?? ''}`)).toEqual(['same:1:1', 'del:2:', 'add::2', 'same:3:3', 'add::4']);
    expect(rows[1].words?.some((w) => w.op === 'del' && w.text === 'x')).toBe(true);
    expect(rows[2].words?.some((w) => w.op === 'add' && w.text === 'y')).toBe(true);
    expect(rows[4].words).toBeUndefined();
    expect(counts(rows)).toEqual({ added: 2, removed: 1 });
  });
  it('folds the unchanged lines far from a change', () => {
    const a = Array.from({ length: 20 }, (_, i) => `l${i}`).join('\n');
    const rows = lineDiff(a, a.replace('l10', 'L10'));
    const shown = foldRows(rows, 2);
    expect(shown.filter(isFold).map((f) => [f.from, f.to])).toEqual([
      [0, 8],
      [14, 21],
    ]);
    expect(foldRows(rows, 2, new Set([0])).filter(isFold)).toHaveLength(1);
    const split = splitRows(shown).filter((r) => !isFold(r));
    expect(split.find((r) => !isFold(r) && r.left?.op === 'del')).toMatchObject({ left: { text: 'l10' }, right: { text: 'L10' } });
  });
});

describe('modes', () => {
  it('follows the type of the attribute unless it names one', () => {
    expect(defaultDiff(undefined)).toBe('words');
    expect(defaultDiff({ type: 'code' })).toBe('code');
    expect(defaultDiff({ type: 'json' })).toBe('json');
    expect(defaultDiff({ type: 'enum' })).toBe('value');
    expect(defaultDiff({ widget: 'textarea' })).toBe('lines');
    expect(defaultDiff({ type: 'code', diff: 'lines' })).toBe('lines');
    expect(defaultDiff({ diff: 'nope' })).toBe('words');
  });
  it('compares JSON with sorted keys', () => {
    expect(jsonText({ b: 1, a: 2 })).toBe(jsonText({ a: 2, b: 1 }));
    expect(jsonText('{"b":1,"a":2}')).toBe(jsonText({ a: 2, b: 1 }));
  });
  it('compares code on its source text, the front matter apart', () => {
    const s = diffSides('code', formatCode({ language: 'go', code: 'func a() {}' }), formatCode({ language: 'go', meta: { file: 'x.go' }, code: 'func b() {}' }));
    expect(s).toMatchObject({ before: 'func a() {}', after: 'func b() {}', language: 'go', meta: ['file: — → x.go'] });
    expect(isInline(s)).toBe(false);
    expect(isInline(diffSides('words', 'a', 'b'))).toBe(true);
  });
});
