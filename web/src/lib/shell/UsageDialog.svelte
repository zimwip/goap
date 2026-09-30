<script lang="ts">
  // Token usage: a modal of its own, mounted once at the shell root. Everyone sees
  // their own consumption; administrators can switch to the whole platform.
  import { usageState, closeUsage } from './usageState.svelte';
  import Icon from './Icon.svelte';
  import TokenUsagePane from '../views/platform/TokenUsagePane.svelte';

  let dialog = $state<HTMLDivElement>();

  $effect(() => {
    if (usageState.open) queueMicrotask(() => dialog?.focus());
  });
</script>

<svelte:window onkeydown={(e) => usageState.open && e.key === 'Escape' && closeUsage()} />

{#if usageState.open}
  <div class="backdrop" role="presentation" onmousedown={closeUsage}>
    <div class="panel" role="dialog" aria-modal="true" aria-label="Token usage" tabindex="-1" bind:this={dialog} onmousedown={(e) => e.stopPropagation()}>
      <div class="head">
        <Icon name="coins" size={16} />
        <h2>Token usage</h2>
        <span class="grow"></span>
        <button type="button" class="ghost small" aria-label="Close token usage" onclick={closeUsage}><Icon name="x" size={14} /></button>
      </div>
      <div class="scroll"><TokenUsagePane /></div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 97;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 0.4);
  }
  .panel {
    width: min(1040px, calc(100vw - 32px));
    height: min(720px, calc(100vh - 48px));
    min-width: 560px;
    min-height: 380px;
    max-width: calc(100vw - 32px);
    max-height: calc(100vh - 48px);
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
    resize: both;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.7rem 1rem;
    border-bottom: 1px solid var(--border);
    background: var(--chrome-2);
  }
  .head h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .grow {
    flex: 1;
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 1rem;
  }
</style>
