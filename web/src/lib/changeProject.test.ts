import { describe, expect, it } from 'vitest';
import { defaultChangeProject, methodologyOffer, moveChoices, movable } from './changeProject';

const projects = [
  { key: 'PROJ-ROOT', label: 'Root' },
  { key: 'A', label: 'A' },
  { key: 'B', label: 'B' },
  { key: 'C', label: 'C' },
];
const applicable = (p: string): string[] => ({ 'PROJ-ROOT': ['m0'], A: ['m1', 'm0'], B: ['m1'], C: ['m2'] })[p] ?? [];

describe('moving a change to another project', () => {
  it('moves only a root change being worked on', () => {
    expect(movable({ status: 'draft' })).toBe(true);
    expect(movable({ status: 'active' })).toBe(true);
    expect(movable({ status: 'committed' })).toBe(false);
    expect(movable({ status: 'applied' })).toBe(false);
    expect(movable({ status: 'abandoned' })).toBe(false);
    expect(movable({ status: 'draft', parentId: 'P' })).toBe(false);
    expect(movable(undefined)).toBe(false);
  });

  it('offers the projects applying the methodology of the change', () => {
    expect(moveChoices({ methodology: 'm1', current: 'A', projects, applicable })).toEqual({ choices: [projects[2]], blocked: '' });
  });

  it('offers every other project to a change with no methodology', () => {
    expect(moveChoices({ current: 'A', projects, applicable }).choices.map((p) => p.key)).toEqual(['PROJ-ROOT', 'B', 'C']);
  });

  it('offers nothing when the source does not list the methodology, or no other project does', () => {
    const fromC = moveChoices({ methodology: 'm1', current: 'C', projects, applicable });
    expect(fromC.choices).toEqual([]);
    expect(fromC.blocked).toContain('C does not list the methodology m1');
    const alone = moveChoices({ methodology: 'm2', current: 'C', projects, applicable });
    expect(alone.choices).toEqual([]);
    expect(alone.blocked).toContain('No other project lists');
  });

  it('starts a new change in the active project, the root for an empty claim', () => {
    expect(defaultChangeProject('B', 'PROJ-ROOT', projects)).toBe('B');
    expect(defaultChangeProject('', 'PROJ-ROOT', projects)).toBe('PROJ-ROOT');
    expect(defaultChangeProject('GONE', 'PROJ-ROOT', projects)).toBe('PROJ-ROOT');
    expect(defaultChangeProject('', 'PROJ-ROOT', [])).toBe('PROJ-ROOT');
  });
});

describe('the methodology of a new change', () => {
  it('preselects the only methodology of the project', () => {
    expect(methodologyOffer(['sdlc'], '', 'A')).toEqual({ options: ['sdlc'], selected: 'sdlc', blocked: '' });
  });
  it('asks to choose among several, and keeps a pick still offered', () => {
    expect(methodologyOffer(['sdlc', 'risk'], '', 'A').selected).toBe('');
    expect(methodologyOffer(['sdlc', 'risk'], 'risk', 'A').selected).toBe('risk');
    expect(methodologyOffer(['sdlc', 'risk'], 'gone', 'A').selected).toBe('');
    expect(methodologyOffer(['sdlc'], 'gone', 'A').selected).toBe('sdlc');
  });
  it('refuses a project that names none', () => {
    const o = methodologyOffer([], 'sdlc', 'A');
    expect(o.selected).toBe('');
    expect(o.blocked).toMatch(/Project A names no methodology/);
  });
});
