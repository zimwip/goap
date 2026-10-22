<script lang="ts">
  // The switch of the modes of the front (ADR 0158), in the header: project, change (enabled while a change is worked
  // in), and for administrators navigation and administration. Each mode shows its own tools (shell/mode.svelte.ts).
  import Icon from './Icon.svelte';
  import { mode, modeEnabled, offeredModes, setMode } from './mode.svelte';
  import { working } from '../stores/workingChange.svelte';

  const modes = $derived(offeredModes());

  function title(m: (typeof modes)[number], enabled: boolean): string {
    if (m.id === 'change' && !enabled) return 'Change mode: select a working change first';
    if (m.id === 'change' && working.change) return `${m.hint} (${working.change.title})`;
    return `${m.id === 'admin' ? 'Administration' : m.label} mode: ${m.hint}`;
  }

  function keydown(e: KeyboardEvent) {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const usable = modes.filter((m) => modeEnabled(m.id));
    const i = usable.findIndex((m) => m.id === mode.current);
    const next = usable[(i + (e.key === 'ArrowRight' ? 1 : -1) + usable.length) % usable.length];
    if (next && setMode(next.id)) (e.currentTarget as HTMLElement).querySelector<HTMLButtonElement>(`[data-mode="${next.id}"]`)?.focus();
    e.preventDefault();
  }
</script>

<div class="modes" role="radiogroup" aria-label="Mode" tabindex="-1" onkeydown={keydown}>
  {#each modes as m (m.id)}
    {@const on = mode.current === m.id}
    {@const enabled = modeEnabled(m.id)}
    <button
      type="button"
      role="radio"
      aria-checked={on}
      data-mode={m.id}
      class:on
      class:admin={m.admin}
      disabled={!enabled}
      tabindex={on ? 0 : -1}
      title={title(m, enabled)}
      onclick={() => setMode(m.id)}
    >
      <Icon name={m.icon} size={14} />
      {#if on}<span class="ml">{m.label}</span>{/if}
    </button>
  {/each}
</div>

<style>
  .modes {
    display: inline-flex;
    flex: none;
    padding: 2px;
    gap: 1px;
    border: 1px solid var(--border);
    border-radius: var(--radius-m);
    background: var(--bg-raised);
  }
  button {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    min-height: 24px;
    padding: 0 0.45rem;
    border: none;
    border-radius: calc(var(--radius-m) - 2px);
    background: none;
    color: var(--text-3);
    font-size: var(--fs-sm);
    font-weight: 500;
    white-space: nowrap;
  }
  button:hover:not(:disabled) {
    background: var(--hover);
    color: var(--text);
  }
  button.on {
    background: var(--surface);
    color: var(--accent);
    box-shadow: var(--shadow-s, 0 1px 2px rgb(0 0 0 / 0.2));
  }
  button:disabled {
    opacity: 0.45;
  }
  /* the administrators' modes stand apart */
  button.admin:not(.admin + .admin) {
    margin-left: 4px;
  }
  /* the modes by their icons, the one the person is in named too (their names in the tooltips); a narrow header: icons only */
  @media (max-width: 1100px) {
    .ml {
      display: none;
    }
  }
</style>
