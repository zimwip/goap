<script lang="ts">
  // "Problems" console: validation of the active tab's draft.
  import Icon from '../../shell/Icon.svelte';
  import { activeDraft } from './activeDraft';
  import { revealIssue } from '../editors/methodologyTabs';
  import type { Section } from '../../methodologyForm';

  const d = $derived(activeDraft());

  const SINGULAR: Record<Section, string> = { agents: 'Agent', actions: 'Action', conditions: 'Condition', goals: 'Goal' };

  /** "actions[2].pre.x" → "Action identify › pre.x". */
  function where(path: string): string {
    if (!d) return path;
    const m = /^(agents|actions|conditions|goals)\[(\d+)\]\.?(.*)$/.exec(path);
    if (m) {
      const section = m[1] as Section;
      const it = d.items(section)[Number(m[2])];
      return `${SINGULAR[section]} ${it?.name || `#${Number(m[2]) + 1}`}${m[3] ? ` › ${m[3]}` : ''}`;
    }
    if (/^(nodeTypes|linkTypes)/.test(path)) return `Domain › ${path}`;
    return path ? `Methodology › ${path}` : 'Methodology';
  }

  const stale = $derived(!!d && d.issues !== null && d.validatedAt !== d.current);
</script>

<div class="console-tools">
  {#if d}
    <strong>{d.label}</strong>
    {#if d.readonly}
      <span class="hint">{d.status === 'published' ? 'published' : 'archived'} version — validation not needed</span>
    {:else if d.issues === null && !d.localIssues.length}
      <span class="hint">not yet validated</span>
    {:else if stale}
      <span class="hint">validation pending (changes in progress)…</span>
    {:else}
      <span class="hint">validated</span>
    {/if}
    <span class="grow"></span>
    {#if !d.readonly && !d.isNew}
      <button type="button" class="small" disabled={!!d.busy} onclick={() => d.validate()}>
        <Icon name="check" size={12} /> Validate
      </button>
    {/if}
  {:else}
    <span class="hint">Open a methodology (or one of its agents, actions…) to see its problems.</span>
  {/if}
</div>
<div class="console-scroll">
  {#if d && !d.readonly}
    {#if d.allIssues.length}
      <ul class="issues">
        {#each d.allIssues as i, k (k)}
          <li>
            <button type="button" class="issue" onclick={() => revealIssue(d, i.norm)}>
              <Icon name="alert" size={13} />
              <span class="msg">{i.message}</span>
              <span class="where">{where(i.norm)}</span>
              {#if i.path}<code class="path">{i.path}</code>{/if}
            </button>
          </li>
        {/each}
      </ul>
    {:else if d.issues !== null}
      <p class="console-empty ok">No problems detected.</p>
    {/if}
  {/if}
</div>

<style>
  .issues {
    list-style: none;
    margin: 0;
    padding: 0.2rem 0;
  }
  .issue {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    text-align: left;
    border: none;
    border-radius: 0;
    background: none;
    font-weight: 400;
    padding: 0.1rem 0.7rem;
    min-height: 22px;
    color: var(--text);
  }
  .issue :global(svg) {
    color: var(--danger);
  }
  .issue:hover:not(:disabled) {
    background: var(--hover);
  }
  .msg {
    flex: none;
    max-width: 60%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .where {
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .path {
    margin-left: auto;
    color: var(--muted);
    font-size: 0.85em;
  }
  .ok {
    color: var(--ok);
    font-style: normal;
  }
</style>
