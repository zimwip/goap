import { beforeEach, describe, expect, it } from 'vitest';
import { aliasAvailable, aliasFlags, modelChoices } from './modelChoices.svelte';

describe('aliasAvailable', () => {
  beforeEach(() => {
    modelChoices.aliases = [];
  });

  it('is false until the choices hold the alias', () => {
    expect(aliasAvailable('assistant')).toBe(false);
    expect(aliasFlags.assistantEnabled).toBe(false);
    expect(aliasFlags.helperEnabled).toBe(false);
  });

  it('follows the aliases the gateway lists', () => {
    modelChoices.aliases = [{ alias: 'assistant', provider: 'fake', model: 'echo', protected: true }];
    expect(aliasAvailable('assistant')).toBe(true);
    expect(aliasFlags.assistantEnabled).toBe(true);
    expect(aliasFlags.helperEnabled).toBe(false);
  });

  it('does not count an alias that targets nothing', () => {
    modelChoices.aliases = [{ alias: 'helper', provider: '', model: '', protected: true }];
    expect(aliasFlags.helperEnabled).toBe(false);
  });
});
