<script lang="ts">
  // The possible next steps of a change (ADR 0097): the steps of its methodology whose conditions all hold now,
  // towards the goal of the change (the end point of the process). Read-only list with one Start per step; the steps
  // that wait are only counted. A step is never described through its actions: the scheduler sequences them.
  import type { StartingPointsResponse, StartingPoint } from '../api';
  import { emptyText, kindText, rolesText, startable, waitingLine, whyText } from '../startingPoints';

  let {
    points,
    error = '',
    busy = '',
    onstart,
    choices = [],
    chosen = '',
    onchoose,
  }: {
    points: StartingPointsResponse | undefined;
    error?: string;
    busy?: string;
    onstart: (p: StartingPoint) => void;
    /** the methodologies of the project, offered when the change has none */
    choices?: string[];
    chosen?: string;
    onchoose?: (name: string) => void;
  } = $props();

  const list = $derived(points?.points ?? []);
  const waiting = $derived(waitingLine(points));
</script>

<section class="steps" aria-label="Possible next steps">
  <h3>Possible next steps</h3>
  {#if choices.length && onchoose}
    <label class="pick">
      This change has no methodology: steps of
      <select value={chosen} onchange={(e) => onchoose?.(e.currentTarget.value)} data-no-pin>
        <option value="">choose a methodology…</option>
        {#each choices as m (m)}<option value={m}>{m}</option>{/each}
      </select>
    </label>
  {/if}
  {#if error}<p class="error">{error}</p>{/if}
  {#if list.length}
    <ul>
      {#each list as p (p.id)}
        <li class:running={p.running}>
          <div class="head">
            <strong>{p.name}</strong>
            <span class="hint">{kindText(p)}</span>
            {#if p.running}<span class="tag">running</span>{/if}
            <span class="grow"></span>
            <button
              type="button"
              class="small primary"
              disabled={!startable(p) || !!busy}
              title={p.running ? 'A process already carries this step out' : p.mayRun ? 'Start this step: the scheduler plans its actions' : rolesText(p)}
              onclick={() => onstart(p)}>{busy === p.id ? 'Starting…' : 'Start'}</button>
          </div>
          {#if p.description}<p class="desc">{p.description}</p>{/if}
          <p class="meta hint">
            possible because: {whyText(p)}
            {#if p.produces?.length}· produces {p.produces.join(', ')}{/if}
            · {rolesText(p)}
          </p>
        </li>
      {/each}
    </ul>
  {:else if !error}
    <p class="hint">{emptyText(points)}</p>
  {/if}
  {#if waiting}
    <details class="waiting">
      <summary>{waiting}</summary>
      <ul>
        {#each points?.blocked ?? [] as b (b.id)}
          <li><code>{b.id}</code> <span class="hint">waits for {(b.missing ?? []).join(', ')}</span></li>
        {/each}
      </ul>
    </details>
  {/if}
</section>

<style>
  .steps {
    margin: 0.75rem 0;
  }
  h3 {
    margin: 0 0 6px;
    font-size: 0.95rem;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .steps > ul > li {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 8px 10px;
  }
  li.running {
    opacity: 0.75;
  }
  .head {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .grow {
    flex: 1;
  }
  .desc,
  .meta {
    margin: 4px 0 0;
  }
  .tag {
    font-size: 0.75rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
  }
  .waiting {
    margin-top: 8px;
  }
  .waiting ul {
    margin-top: 4px;
    gap: 2px;
  }
</style>
