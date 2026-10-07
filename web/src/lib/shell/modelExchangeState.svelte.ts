// The prompt modal: what was sent to the model in a call, and what it answered. One global dialog, mounted at the shell
// root (ModelExchangeDialog.svelte).
import type { Int64, LLMCall } from '../api';

/** Where the exchange is read: the log of the change (an engine call of a change, ADR 0059) or the gateway (every other call, ADR 0089). */
export type ModelExchangeRequest = {
  /** line shown in the title: action, model */
  label: string;
  /** the ledger row, for the header (the gateway's answer carries its own) */
  meta?: LLMCall;
} & (
  | {
      changeId: string;
      processId: string;
      /** step of the process and position of the call in it, as the token console lists them */
      step: number;
      call: number;
    }
  | { seq: Int64 }
);

export const modelExchangeState = $state<{ current: ModelExchangeRequest | null }>({ current: null });

export function openModelExchange(req: ModelExchangeRequest): void {
  modelExchangeState.current = req;
}

export function closeModelExchange(): void {
  modelExchangeState.current = null;
}
