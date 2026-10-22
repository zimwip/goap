import { describe, expect, it } from 'vitest';
import { TypeCatalog } from './stores/types.svelte';
import { cypherSuggestions, nameOf, variableTypes, type QueryScope } from './cypherComplete';

const attr = (name: string, type = 'string') => ({ attribute: { name, type } });
const cat = new TypeCatalog(
  [
    { ref: 'alm@Requirement', attributes: [attr('title'), attr('priority', 'enum')] },
    { ref: 'alm@Need', attributes: [attr('title')] },
    { ref: 'alm@TestCase', attributes: [attr('title')] },
    { ref: 'alm@Function', attributes: [attr('name')] },
    { ref: 'alm@SafetyRequirement', ancestors: ['alm@Requirement'], attributes: [attr('title'), attr('priority', 'enum'), attr('asil', 'enum')] },
    { ref: 'code@File', attributes: [attr('path')] },
  ],
  [
    { ref: 'alm@satisfies', from: 'alm@Requirement', to: 'alm@Need', attributes: [attr('rationale')] },
    { ref: 'alm@verifies', from: 'alm@TestCase', to: 'alm@Requirement' },
    { ref: 'alm@realizes', from: 'alm@Function', to: 'alm@Requirement' },
    { ref: 'code@contains' },
  ],
);
const scope: QueryScope = { cat, namespace: 'alm' };

/** The labels offered with the cursor at `|`, best ranked first. */
function at(q: string, s: QueryScope = scope, explicit = false): string[] {
  const pos = q.indexOf('|');
  const res = cypherSuggestions(q.replace('|', ''), pos, s, explicit);
  return (res?.options ?? []).slice().sort((a, b) => (b.boost ?? 0) - (a.boost ?? 0)).map((o) => o.label);
}

describe('nameOf', () => {
  it('names a type of the namespace read bare', () => expect(nameOf('alm@Need', 'alm')).toBe('Need'));
  it('escapes a type of another namespace', () => expect(nameOf('code@File', 'alm')).toBe('`code@File`'));
});

describe('cypherSuggestions', () => {
  it('offers the node types after a colon in a node pattern', () => {
    const got = at('MATCH (r:|');
    expect(got).toContain('Requirement');
    expect(got).toContain('`code@File`');
    expect(got.indexOf('Requirement')).toBeLessThan(got.indexOf('`code@File`'));
  });

  it('starts the word where the type being typed starts', () => {
    expect(cypherSuggestions('MATCH (r:Req', 12, scope)?.from).toBe(9);
  });

  it('offers the link types a node of the type on the left holds, outgoing ones first', () => {
    const got = at('MATCH (r:Requirement)-[:|');
    expect(got.slice(0, 1)).toEqual(['satisfies']);
    expect(got).toContain('verifies');
    expect(got).toContain('`code@contains`');
    expect(got.indexOf('verifies')).toBeLessThan(got.indexOf('`code@contains`'));
  });

  it('offers only the entering link types behind an arrow pointing left', () => {
    const got = at('MATCH (r:Requirement)<-[:|');
    expect(got.slice(0, 2).sort()).toEqual(['realizes', 'verifies']);
    expect(got).not.toContain('satisfies');
  });

  it('reads the type of a variable labelled earlier or bound', () => {
    expect(at('MATCH (r:Requirement) MATCH (r)-[:|')[0]).toBe('satisfies');
    expect(at('MATCH (n)-[l:|', { ...scope, bound: { n: 'alm@TestCase' } })[0]).toBe('verifies');
  });

  it('offers the node types a link reaches first, by its direction', () => {
    expect(at('MATCH (r)-[:satisfies]->(n:|')[0]).toBe('Need');
    expect(at('MATCH (r)<-[:verifies]-(t:|')[0]).toBe('TestCase');
    expect(at('MATCH (r:Requirement)-[:verifies]->(x:|').slice(0, 2).sort()).toEqual(['Requirement', 'SafetyRequirement']);
  });

  it('reads the link types of the node on the left when the link names none', () => {
    expect(at('MATCH (r:Requirement)-[]->(n:|')[0]).toBe('Need');
  });

  it('offers the attributes and fields of a node variable', () => {
    const got = at('MATCH (r:SafetyRequirement) WHERE r.|');
    expect(got.slice(0, 3).sort()).toEqual(['asil', 'priority', 'title']);
    expect(got).toContain('key');
    expect(got).toContain('state');
  });

  it('offers the attributes of a link variable', () => {
    const got = at('MATCH (r)-[l:satisfies]->(n) RETURN l.|');
    expect(got[0]).toBe('rationale');
    expect(got).toContain('type');
    expect(got).not.toContain('key');
  });

  it('offers the attributes in the property map of a pattern', () => {
    expect(at('MATCH (f:Function {|')[0]).toBe('name');
    expect(at("MATCH (r:Requirement {priority: 'high', |")[0]).toMatch(/^(priority|title)$/);
  });

  it('offers keywords, functions and variables for a word', () => {
    const got = at('MATCH (r:Requirement) WH|');
    expect(got).toContain('WHERE');
    expect(at('MATCH (r:Requirement) RETURN co|')).toContain('count');
    expect(at('MATCH (req:Requirement) RETURN r|')[0]).toBe('req');
  });

  it('stays quiet in a string and on nothing typed unless asked', () => {
    expect(cypherSuggestions("MATCH (r) WHERE r.title = 'a (b:", 33, scope)).toBeNull();
    expect(cypherSuggestions('MATCH (r) ', 10, scope)).toBeNull();
    expect(cypherSuggestions('MATCH (r) ', 10, scope, true)?.options.length).toBeGreaterThan(0);
  });

  it('collects the types of the variables, escaped labels included', () => {
    const v = variableTypes('MATCH (f:`code@File`)-[c:contains]-(r:Requirement)', scope);
    expect(v.nodes.get('f')).toBe('code@File');
    expect(v.nodes.get('r')).toBe('alm@Requirement');
    expect(v.links.get('c')).toBe('code@contains');
  });
});
