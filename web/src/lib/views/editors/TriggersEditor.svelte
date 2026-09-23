<script lang="ts">
  // "Triggers" section of the agent editor.
  import CodeEditor from '../../components/CodeEditor.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import { TRIGGER_EVENTS } from '../../api';
  import { emptyTrigger, moveItem, TRIGGER_TARGETS, type TriggerForm } from '../../methodologyForm';

  let {
    triggers = $bindable(),
    path,
    goals,
    bad,
    count,
    readonly = false,
  }: {
    triggers: TriggerForm[];
    /** "agents[i].triggers" */
    path: string;
    /** goals of the agent */
    goals: string[];
    bad: (path: string, exact?: boolean) => boolean;
    count: (path: string) => number;
    readonly?: boolean;
  } = $props();

  const EVENT_LABELS: Record<string, string> = {
    'change.created': 'change.created — change created',
    'change.applied': 'change.applied — change applied',
    'change.item_added': 'change.item_added — item added to a change',
    'process.completed': 'process.completed — run completed',
    'process.failed': 'process.failed — run failed',
    'process.stuck': 'process.stuck — run stuck (no plan)',
    'methodology.published': 'methodology.published — methodology published',
  };
  const TARGET_LABELS: Record<string, string> = {
    new_change: 'new change (latest baseline)',
    event_change: 'change from the event',
  };

  function add() {
    triggers.push(emptyTrigger());
  }
</script>

<section class="card" data-path={path}>
  <div class="head">
    <h3>Triggers</h3>
    <span class="hint">automatic runs of the agent, outside the intent loop</span>
    <span class="grow"></span>
    {#if !readonly}<button type="button" class="small" onclick={add}>+ Trigger</button>{/if}
  </div>
  {#each triggers as t, j}
    {@const p = `${path}[${j}]`}
    {@const n = count(p)}
    <div class="trig" class:has-issues={n > 0} class:off={!t.enabled} data-path={p}>
      <div class="trow">
        <label class="check">
          <input type="checkbox" bind:checked={t.enabled} aria-label="Enabled" />
          enabled
        </label>
        <input
          type="text"
          class="mono name"
          aria-label="Trigger name"
          placeholder="name"
          bind:value={t.name}
          class:bad={bad(`${p}.name`)}
          data-path="{p}.name"
        />
        <select aria-label="Type" bind:value={t.type} class:bad={bad(`${p}.type`)} data-path="{p}.type">
          <option value="event">event</option>
          <option value="schedule">schedule (cron)</option>
        </select>
        <span class="grow"></span>
        {#if n}<span class="count">{n} issue{n > 1 ? 's' : ''}</span>{/if}
        {#if !readonly}
          <RowTools
            index={j}
            count={triggers.length}
            label="the trigger"
            onmove={(d) => moveItem(triggers, j, d)}
            onremove={() => triggers.splice(j, 1)}
          />
        {/if}
      </div>
      <div class="field">
        <input
          type="text"
          aria-label="Description"
          placeholder="Description"
          bind:value={t.description}
          class:bad={bad(`${p}.description`)}
          data-path="{p}.description"
        />
      </div>
      {#if t.type === 'schedule'}
        <div class="field">
          <label for="{p}-cron">Schedule <span class="opt">(5-field cron, UTC)</span></label>
          <input
            id="{p}-cron"
            type="text"
            class="mono"
            placeholder="0 6 * * 1-5"
            bind:value={t.schedule}
            class:bad={bad(`${p}.schedule`)}
            data-path="{p}.schedule"
          />
          <p class="hint">minute hour day-of-month month day-of-week — e.g. <code>0 6 * * 1-5</code>: 6am UTC Monday through Friday; <code>*/15 * * * *</code>: every 15 min.</p>
        </div>
      {:else}
        <div class="grid">
          <div class="field">
            <label for="{p}-event">Event</label>
            <select id="{p}-event" bind:value={t.event} class:bad={bad(`${p}.event`)} data-path="{p}.event">
              {#if t.event && !(TRIGGER_EVENTS as readonly string[]).includes(t.event)}<option value={t.event}>{t.event} (unknown)</option>{/if}
              {#each TRIGGER_EVENTS as e (e)}<option value={e}>{EVENT_LABELS[e]}</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="{p}-filter">Filter <span class="opt">(CEL, optional)</span></label>
          <CodeEditor
            id="{p}-filter"
            bind:value={t.filter}
            language="cel"
            lineNumbers={false}
            {readonly}
            label="CEL filter"
            minHeight="1.9rem"
            maxHeight="8rem"
            placeholder={'event.change.methodology == "impact-analysis"'}
            bad={bad(`${p}.filter`)}
            path="{p}.filter"
          />
          <p class="hint">
            Variables: <code>event.type</code>, <code>event.change.{'{'}id, title, status, methodology, goal, baseline{'}'}</code>,
            <code>event.process.{'{'}id, agent, goal, status{'}'}</code>.
          </p>
        </div>
      {/if}
      <div class="grid">
        <div class="field">
          <label for="{p}-goal">Goal</label>
          <select id="{p}-goal" bind:value={t.goal} class:bad={bad(`${p}.goal`)} data-path="{p}.goal">
            <option value="">— agent's single goal, or intent loop —</option>
            {#if t.goal && !goals.includes(t.goal)}<option value={t.goal}>{t.goal} (unknown)</option>{/if}
            {#each goals as g (g)}<option value={g}>{g}</option>{/each}
          </select>
        </div>
        <div class="field">
          <label for="{p}-target">Target</label>
          <select id="{p}-target" bind:value={t.target} class:bad={bad(`${p}.target`)} data-path="{p}.target">
            {#each TRIGGER_TARGETS as x (x)}<option value={x}>{TARGET_LABELS[x]}</option>{/each}
          </select>
        </div>
        <div class="field">
          <label for="{p}-roles">Roles <span class="opt">(comma-separated)</span></label>
          <input
            id="{p}-roles"
            type="text"
            class="mono"
            placeholder="reviewer, approver"
            bind:value={t.roles}
            class:bad={bad(`${p}.roles`)}
            data-path="{p}.roles"
          />
        </div>
      </div>
      <div class="field">
        <label for="{p}-intent">Intent passed to the process</label>
        <textarea id="{p}-intent" rows="2" bind:value={t.intent} class:bad={bad(`${p}.intent`)} data-path="{p}.intent"></textarea>
      </div>
    </div>
  {:else}
    <p class="empty">No triggers: the agent only runs on demand.</p>
  {/each}
</section>

<style>
  .head {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin-bottom: 0.5rem;
  }
  .head h3 {
    margin: 0;
  }
  .trig {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.5rem 0.6rem 0.1rem;
    margin-bottom: 0.5rem;
  }
  .trig.off {
    opacity: 0.75;
    border-style: dashed;
  }
  .trig.has-issues {
    border-color: var(--danger);
    box-shadow: inset 3px 0 0 var(--danger);
  }
  .trow {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin-bottom: 0.45rem;
  }
  .trow .name {
    width: 14rem;
  }
  .trow select {
    width: auto;
  }
  .count {
    font-size: 0.8rem;
    font-weight: 700;
    color: var(--danger);
  }
</style>
