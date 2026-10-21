import { describe, expect, it } from 'vitest';
import { editorLanguage, formatCode, parseCode, readCode, type CodeValue } from './codeText';

describe('code text', () => {
  it('writes the front matter then the fenced block, as the server does', () => {
    expect(formatCode({ language: 'go', code: 'func F() {\n\treturn\n}' })).toBe('---\nlanguage: go\n---\n```go\nfunc F() {\n\treturn\n}\n```\n');
    expect(formatCode({ language: 'markdown', meta: { path: 'a b.md', note: ' x' }, code: '````js\nx\n````' })).toBe(
      '---\nlanguage: markdown\nnote: " x"\npath: a b.md\n---\n`````markdown\n````js\nx\n````\n`````\n',
    );
  });

  it('reads back what it writes', () => {
    for (const v of <CodeValue[]>[
      { language: 'python', meta: {}, code: 'def f():\n    ```\n    pass' },
      { language: '', meta: {}, code: '' },
      { language: 'c', meta: { file: 'x.c', quote: 'say "hi"' }, code: 'int x;' },
    ])
      expect(parseCode(formatCode(v))).toEqual(v);
  });

  it('reads a plain text, a block alone and CRLF', () => {
    expect(parseCode('plain\ntext')).toEqual({ language: '', meta: {}, code: 'plain\ntext' });
    expect(parseCode('```rust\nfn main() {}\n```')).toEqual({ language: 'rust', meta: {}, code: 'fn main() {}' });
    expect(parseCode("---\r\nlanguage: 'it''s'\r\n---\r\n\r\n~~~\r\nx\r\n~~~\r\n")).toEqual({ language: "it's", meta: {}, code: 'x' });
  });

  it('refuses a malformed value, readCode falls back to the text', () => {
    for (const bad of ['---\nlanguage: go\n', '---\nnot a pair\n---\nx', '---\n---\n```\nx\n```\ntrailing']) expect(() => parseCode(bad)).toThrow();
    expect(readCode('---\nlanguage: go\n')).toMatchObject({ code: '---\nlanguage: go\n', error: expect.any(String) });
  });

  it('maps a language to the highlighting of the editor', () => {
    expect(editorLanguage('go')).toBe('go');
    expect(editorLanguage('C++')).toBe('cpp');
    expect(editorLanguage('ts')).toBe('typescript');
    expect(editorLanguage('cobol')).toBe('text');
  });
});
