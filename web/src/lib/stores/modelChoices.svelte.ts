// Models the current user may pick for an llm action: the aliases and the
// enabled catalog models of the gateway they have the role for.
import { models, onTokenChange, type AvailableModel, type ModelAlias } from '../api';

export const modelChoices = $state({
  models: [] as AvailableModel[],
  aliases: [] as ModelAlias[],
  loaded: false,
  error: '',
});

let pending: Promise<void> | undefined;

export function refreshModelChoices(): Promise<void> {
  pending ??= models
    .listAvailable()
    .then((r) => {
      modelChoices.models = r.models ?? [];
      modelChoices.aliases = r.aliases ?? [];
      modelChoices.error = '';
    })
    .catch((e) => {
      modelChoices.error = e instanceof Error ? e.message : String(e);
    })
    .finally(() => {
      modelChoices.loaded = true;
      pending = undefined;
    });
  return pending;
}

/** Is `value` ("" = default alias, an alias, or "provider/model") one of the choices? */
export function isAvailableModel(value: string): boolean {
  const v = value.trim() || 'default';
  return modelChoices.aliases.some((a) => a.alias === v) || modelChoices.models.some((m) => `${m.provider}/${m.model}` === v);
}

/** Is `value` ("" = default alias, or an alias) one of the configured aliases? Administrators own what an
 * alias resolves to; authors only ever pick or type an alias name, never a raw provider/model. */
export function isAvailableAlias(value: string): boolean {
  const v = value.trim() || 'default';
  return modelChoices.aliases.some((a) => a.alias === v);
}

/** The protected aliases the platform resolves itself (ADR 0084): the conversational assistant and the field helper. */
export const ASSISTANT_ALIAS = 'assistant';
export const HELPER_ALIAS = 'helper';

/** Can `alias` be used right now? ListModels only returns the aliases that resolve to a model the caller may use, so a
 * protected alias nothing is configured for (or whose model is gone) is not available. False until the choices load. */
export function aliasAvailable(alias: string): boolean {
  return modelChoices.aliases.some((a) => a.alias === alias && !!a.provider && !!a.model);
}

/** Whether the assistant and the helper can be offered: reactive (read it in a template or an effect); the choices are
 * refreshed when the model configuration changes (flux reducers), call `refreshModelChoices` once to load them. */
export const aliasFlags = {
  get assistantEnabled(): boolean {
    return aliasAvailable(ASSISTANT_ALIAS);
  },
  get helperEnabled(): boolean {
    return aliasAvailable(HELPER_ALIAS);
  },
};

onTokenChange(() => void refreshModelChoices());
