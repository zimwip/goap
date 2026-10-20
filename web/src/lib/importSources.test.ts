import { describe, expect, it } from 'vitest';
import { initialValues, jobSubject, paramFields, runParams, statsLine } from './importSources';
import type { Importer } from './api';

const git: Importer = {
  domain: 'code',
  name: 'git',
  params: [
    { attribute: { name: 'max', type: 'number', order: 5 } },
    { attribute: { name: 'repository', label: 'Repository', order: 1 } },
    { attribute: { name: 'rev', defaultValue: 'HEAD', order: 3 } },
    { attribute: { name: 'symbols', type: 'boolean', order: 6 } },
    { attribute: { name: 'mode', type: 'enum', enum: 'mode', order: 4 }, values: [{ value: 'full', label: 'Full' }, { value: 'fast' }] },
  ],
};

describe('import sources', () => {
  it('turns the declared parameters into fields, in their order', () => {
    const f = paramFields(git);
    expect(f.map((x) => x.name)).toEqual(['repository', 'rev', 'mode', 'max', 'symbols']);
    expect(f[0].label).toBe('Repository');
    expect(f[2]).toMatchObject({ kind: 'enum', options: [{ value: 'full', label: 'Full' }, { value: 'fast', label: 'fast' }] });
    expect(initialValues(f)).toEqual({ repository: '', rev: 'HEAD', mode: '', max: '', symbols: false });
  });

  it('gives the values of a run: empty left out, numbers as numbers', () => {
    const f = paramFields(git);
    expect(runParams(f, { repository: ' ~/src/linux ', rev: '', max: '100', symbols: true, mode: '' })).toEqual({
      repository: '~/src/linux',
      max: 100,
      symbols: true,
    });
    expect(runParams(f, { max: 'many' })).toBe('max: a number is expected');
  });

  it('reads a run', () => {
    expect(statsLine({ stats: { commits: '12', created: '40', removed: '0' } })).toBe('12 commits, 40 created');
    expect(jobSubject({ params: { repository: '/src/jq', name: 'jq' } })).toBe('/src/jq');
  });
});
