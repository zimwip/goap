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

onTokenChange(() => void refreshModelChoices());
