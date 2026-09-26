<script lang="ts">
  import { shortId, type BoardIssue } from '../api';

  let {
    issues,
    onitem,
    sections = false,
  }: {
    issues: BoardIssue[];
    onitem?: (id: string) => void;
    /** split errors and warnings under their own headings (warnings never block a run) */
    sections?: boolean;
  } = $props();

  const HINTS: Record<string, string> = {
    outdated: 'The change is based on node versions that moved: rebase it.',
  };

  interface Group {
    severity: string;
    code: string;
    issues: BoardIssue[];
  }

  const isWarning = (i: BoardIssue) => i.severity === 'warning';

  function group(list: BoardIssue[]): Group[] {
    const by = new Map<string, Group>();
    for (const i of list) {
      const key = `${i.severity}|${i.code}`;
      let g = by.get(key);
      if (!g) by.set(key, (g = { severity: i.severity ?? 'error', code: i.code ?? '', issues: [] }));
      g.issues.push(i);
    }
    return [...by.values()];
  }

  const errors = $derived(group(issues.filter((i) => !isWarning(i))));
  const warnings = $derived(group(issues.filter(isWarning)));
  const all = $derived([...errors, ...warnings]);
</script>

{#snippet rows(groups: Group[])}
  <ul class="issues">
    {#each groups as g (g.severity + g.code)}
      <li class:warning={g.severity === 'warning'}>
        <details>
          <summary>
            <span class="sev {g.severity}">{g.severity}</span>
            <span class="chip">{g.issues.length} × {g.code}</span>
            <span class="msg">{g.issues[0].message}</span>
          </summary>
          {#if HINTS[g.code]}<p class="hint">{HINTS[g.code]}</p>{/if}
          <ul class="all">
            {#each g.issues as i, k (k)}
              <li>
                <span class="msg">{i.message}</span>
                {#if i.item}
                  <span class="ref">
                    item
                    {#if onitem}<button type="button" class="link mono" onclick={() => onitem?.(i.item ?? '')}>{shortId(i.item)}</button>
                    {:else}<code>{shortId(i.item)}</code>{/if}
                    {#if i.culprit && i.culprit !== i.item}
                      · blame
                      {#if onitem}<button type="button" class="link mono" onclick={() => onitem?.(i.culprit ?? '')}>{shortId(i.culprit)}</button>
                      {:else}<code>{shortId(i.culprit)}</code>{/if}
                    {/if}
                  </span>
                {/if}
              </li>
            {/each}
          </ul>
        </details>
      </li>
    {/each}
  </ul>
{/snippet}

{#if sections}
  {#if errors.length}
    <h4 class="head error">Errors</h4>
    {@render rows(errors)}
  {/if}
  {#if warnings.length}
    <h4 class="head warn">Warnings</h4>
    <p class="hint">Warnings do not block the run.</p>
    {@render rows(warnings)}
  {/if}
{:else}
  {@render rows(all)}
{/if}

<style>
  .issues {
    list-style: none;
    padding: 0;
    margin: 8px 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  summary {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px;
    cursor: pointer;
  }
  .all {
    list-style: none;
    margin: 4px 0 0 14px;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 3px;
    max-height: 220px;
    overflow: auto;
  }
  .sev {
    font-size: 0.75em;
    text-transform: uppercase;
    color: var(--danger);
  }
  li.warning .sev {
    color: var(--warn);
  }
  li.warning .chip {
    border-style: dashed;
    color: var(--warn);
  }
  .chip {
    font-size: 0.75em;
    padding: 0 6px;
    border: 1px solid var(--border, currentColor);
    border-radius: 8px;
  }
  .ref,
  .hint {
    color: var(--muted);
    font-size: 0.85em;
  }
  .hint {
    margin: 4px 0 4px 14px;
  }
  .head {
    margin: 8px 0 0;
    font-size: 0.85em;
  }
  .head.error {
    color: var(--danger);
  }
  .head.warn {
    color: var(--warn);
  }
</style>
