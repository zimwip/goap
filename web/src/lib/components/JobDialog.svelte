<script lang="ts">
  // The detail of one job (ADR 0149), opened by a click on a job wherever it is listed (status bar, Jobs tab): its
  // progress, its facts and parameters, its counters, its error, the file it wrote, the change it created and its whole
  // log, live from the job stream while it runs; the form of the request it waits on, answered here by its person.
  import type { Job } from '../api';
  import { errorMessage } from '../api';
  import Icon from '../shell/Icon.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { notify } from '../shell/workbench.svelte';
  import { answerJob, cancelJob, downloadJobResult, knownJobs } from '../stores/jobs.svelte';
  import { fraction, isActive, jobChange, jobFacts, jobTitle, logLines, paramLines, pendingRequest, remaining, size } from '../jobView';
  import FormRenderer from '../forms/FormRenderer.svelte';
  import type { FormValues } from '../forms/types';
  import { me } from '../stores/session.svelte';

  let { job: given, onclose }: { job: Job; onclose?: () => void } = $props();

  const job = $derived(knownJobs.get(given.id ?? '') ?? given);
  let now = $state(Date.now());
  const part = $derived(fraction(job));
  const active = $derived(isActive(job));
  const change = $derived(jobChange(job));
  const left = $derived(remaining(job, now));
  const facts = $derived(jobFacts(job, now));
  const params = $derived(paramLines(job));
  const counters = $derived(Object.entries(job.stats ?? {}));
  const lines = $derived(logLines(job));
  const asked = $derived(pendingRequest(job));
  /** only the person the job runs for answers it */
  const mine = $derived(!!job.by && job.by === me());
  let answering = $state(false);
  let answerError = $state('');
  let busy = $state(false);
  let logBox = $state<HTMLElement | undefined>();
  /** the log follows its newest line while the person has not scrolled up */
  let follow = true;

  $effect(() => {
    if (!active) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  $effect(() => {
    void lines.length;
    if (follow && logBox) logBox.scrollTop = logBox.scrollHeight;
  });

  function scrolled() {
    if (logBox) follow = logBox.scrollTop + logBox.clientHeight >= logBox.scrollHeight - 8;
  }

  async function act(fn: (j: Job) => Promise<void>) {
    busy = true;
    try {
      await fn(job);
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      busy = false;
    }
  }

  async function answer(values: FormValues) {
    if (!asked) return;
    answering = true;
    answerError = '';
    try {
      await answerJob(job, asked.id, values);
    } catch (e) {
      answerError = errorMessage(e);
    } finally {
      answering = false;
    }
  }

  function openChange() {
    if (!change) return;
    openTab({ kind: 'change', params: { id: change.id } }, { pin: true });
    onclose?.();
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose?.()} />

<div class="backdrop" role="presentation" onmousedown={() => onclose?.()}>
  <div class="panel" role="dialog" aria-modal="true" aria-label="Job {jobTitle(job)}" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
    <header class="head">
      <h2 title={job.kind}>{jobTitle(job)}</h2>
      <StatusBadge status={job.status ?? ''} />
      <span class="grow"></span>
      <button type="button" class="ghost small" aria-label="Close" onclick={() => onclose?.()}><Icon name="x" size={14} /></button>
    </header>
    <div class="body">
      {#if active}
        <div class="bar" role="progressbar" aria-label="Progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow={part === undefined ? undefined : Math.round(part * 100)}>
          <span class:indeterminate={part === undefined} style:width={part === undefined ? undefined : `${part * 100}%`}></span>
        </div>
      {/if}
      {#if job.progress || left}
        <p class="line">
          {job.progress ?? ''}{part !== undefined && active ? ` (${Math.round(part * 100)} %)` : ''}{#if left}<span class="hint"> · {left}</span>{/if}
        </p>
      {/if}
      {#if job.status === 'queued'}<p class="hint">Waiting for a worker.</p>{/if}
      {#if job.cancelRequested && active}<p class="hint">Stopping…</p>{/if}
      {#if job.error}<div class="alert small line">{job.error}</div>{/if}
      {#if asked}
        <section class="ask" aria-label="Question of the job">
          {#if mine}
            <p class="hint">The job waits for your answer to go on.</p>
            {#key asked.id}
              <FormRenderer form={asked.form} busy={answering} serverError={answerError} onsubmit={(v) => void answer(v)} submitLabel={asked.form.submit || 'Answer'} />
            {/key}
          {:else}
            <p class="hint">Waiting for {job.by} to answer: {asked.form.title || 'a question'}.</p>
          {/if}
        </section>
      {/if}

      <dl class="facts">
        {#each facts as f (f.label)}
          <dt>{f.label}</dt>
          <dd>{f.value}</dd>
        {/each}
        {#if job.result?.name}
          <dt>Result</dt>
          <dd>{job.result.name} ({size(job.result.size)})</dd>
        {/if}
      </dl>

      {#if params.length}
        <h3>Parameters</h3>
        <dl class="facts">
          {#each params as p (p.name)}
            <dt>{p.name}</dt>
            <dd><code>{p.value}</code></dd>
          {/each}
        </dl>
      {/if}

      {#if counters.length}
        <h3>Counters</h3>
        <dl class="facts">
          {#each counters as [k, n] (k)}
            <dt>{k}</dt>
            <dd>{n}</dd>
          {/each}
        </dl>
      {/if}

      <h3>Log</h3>
      {#if lines.length}
        <ol class="log" bind:this={logBox} onscroll={scrolled} aria-label="Log of the job">
          {#each lines as l, i (i)}
            <li><span class="at">{l.at}</span> {l.text}</li>
          {/each}
        </ol>
      {:else}
        <p class="hint">The run said nothing yet.</p>
      {/if}
    </div>
    <footer class="foot">
      <span class="grow"></span>
      {#if change}
        <button type="button" class="ghost" title="Open the change {change.title}" onclick={openChange}><Icon name="branch" size={14} /> Open change</button>
      {/if}
      {#if job.status === 'done' && job.result?.name}
        <button type="button" class="ghost" disabled={busy} onclick={() => void act(downloadJobResult)}><Icon name="download" size={14} /> Download</button>
      {/if}
      {#if active && !job.cancelRequested}
        <button type="button" class="ghost" disabled={busy} onclick={() => void act(cancelJob)}><Icon name="stop" size={14} /> Cancel job</button>
      {/if}
      <button type="button" onclick={() => onclose?.()}>Close</button>
    </footer>
  </div>
</div>

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
    width: min(720px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
    outline: none;
  }
  .head,
  .foot {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0.6rem 1rem;
    background: var(--chrome-2);
  }
  .head {
    border-bottom: 1px solid var(--border);
  }
  .foot {
    border-top: 1px solid var(--border);
  }
  .head h2 {
    margin: 0;
    font-size: 1rem;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .grow {
    flex: 1;
  }
  .foot button {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.8rem 1rem;
    display: grid;
    gap: 0.5rem;
    align-content: start;
  }
  .body h3 {
    margin: 0.4rem 0 0;
    font-size: 0.85rem;
  }
  .ask {
    padding: 0.4rem 0.7rem 0.6rem;
    border: 1px solid var(--accent);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .ask .hint {
    margin: 0;
  }
  .line {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.15rem 0.8rem;
    margin: 0;
    font-size: 0.85rem;
  }
  .facts dt {
    color: var(--muted);
  }
  .facts dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .log {
    list-style: none;
    margin: 0;
    padding: 0.3rem 0.45rem;
    max-height: 18rem;
    overflow: auto;
    background: var(--neutral-soft);
    border-radius: 4px;
    font-family: var(--mono, monospace);
    font-size: 0.78rem;
  }
  .log li {
    overflow-wrap: anywhere;
  }
  .log .at {
    color: var(--muted);
  }
  .bar {
    height: 6px;
    border-radius: 3px;
    background: var(--neutral-soft);
    overflow: hidden;
    position: relative;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.3s ease;
  }
  .bar .indeterminate {
    position: absolute;
    width: 30%;
    animation: slide 1.2s ease-in-out infinite;
  }
  @keyframes slide {
    from {
      left: -30%;
    }
    to {
      left: 100%;
    }
  }
</style>
