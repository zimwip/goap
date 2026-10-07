// Is the conversational assistant available to the caller? It needs the protected "assistant" alias to resolve to a
// model the caller may use (ADR 0084); the helper (ADR 0086) has its own flag.
import { aliasFlags, modelChoices, refreshModelChoices } from '../stores/modelChoices.svelte';

/** Why the assistant is off, shown wherever its entry is disabled. */
export const ASSISTANT_OFF = 'No model is configured for the assistant alias';

export function assistantEnabled(): boolean {
  if (!modelChoices.loaded) void refreshModelChoices();
  return aliasFlags.assistantEnabled;
}
