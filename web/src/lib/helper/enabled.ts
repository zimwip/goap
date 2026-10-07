// Is the contextual helper available to the caller? It needs the protected "helper" alias to resolve to a model the
// caller may use (ADR 0084); loading the choices is this module's job, the flag itself is the platform's.
import { aliasFlags, modelChoices, refreshModelChoices } from '../stores/modelChoices.svelte';

export function helperEnabled(): boolean {
  if (!modelChoices.loaded) void refreshModelChoices();
  return aliasFlags.helperEnabled;
}
