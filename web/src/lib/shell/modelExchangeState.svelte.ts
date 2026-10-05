// The prompt modal: what was sent to the model in a call of a run, and what it answered. One global dialog, mounted at
// the shell root (ModelExchangeDialog.svelte).

export interface ModelExchangeRequest {
  changeId: string;
  processId: string;
  /** step of the process and position of the call in it, as the token console lists them */
  step: number;
  call: number;
  /** line shown in the title: action, model */
  label: string;
}

export const modelExchangeState = $state<{ current: ModelExchangeRequest | null }>({ current: null });

export function openModelExchange(req: ModelExchangeRequest): void {
  modelExchangeState.current = req;
}

export function closeModelExchange(): void {
  modelExchangeState.current = null;
}
