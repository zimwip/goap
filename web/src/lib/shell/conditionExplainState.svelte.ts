// The condition solver modal: how a condition of a run's world state got its value. One global dialog, mounted at
// the shell root (ConditionExplainDialog.svelte).

export const conditionExplainState = $state<{ current: { processId: string; condition: string } | null }>({ current: null });

export function openConditionExplain(processId: string, condition: string): void {
  conditionExplainState.current = { processId, condition };
}

export function closeConditionExplain(): void {
  conditionExplainState.current = null;
}
