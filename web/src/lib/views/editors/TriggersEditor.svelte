<script lang="ts">
  // Section « Déclencheurs » de l'éditeur d'agent.
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
    /** « agents[i].triggers » */
    path: string;
    /** objectifs de l'agent */
    goals: string[];
    bad: (path: string, exact?: boolean) => boolean;
    count: (path: string) => number;
    readonly?: boolean;
  } = $props();

  const EVENT_LABELS: Record<string, string> = {
    'change.created': 'change.created — changement créé',
    'change.applied': 'change.applied — changement appliqué',
    'change.item_added': 'change.item_added — item ajouté à un changement',
    'process.completed': 'process.completed — exécution terminée',
    'process.failed': 'process.failed — exécution en échec',
    'process.stuck': 'process.stuck — exécution bloquée (aucun plan)',
    'methodology.published': 'methodology.published — méthodologie publiée',
  };
  const TARGET_LABELS: Record<string, string> = {
    new_change: 'nouveau changement (dernier référentiel)',
    event_change: "changement de l'événement",
  };

  function add() {
    triggers.push(emptyTrigger());
  }
</script>

<section class="card" data-path={path}>
  <div class="head">
    <h3>Déclencheurs</h3>
    <span class="hint">exécutions automatiques de l'agent, hors boucle d'intention</span>
    <span class="grow"></span>
    {#if !readonly}<button type="button" class="small" onclick={add}>+ Déclencheur</button>{/if}
  </div>
  {#each triggers as t, j}
    {@const p = `${path}[${j}]`}
    {@const n = count(p)}
    <div class="trig" class:has-issues={n > 0} class:off={!t.enabled} data-path={p}>
      <div class="trow">
        <label class="check">
          <input type="checkbox" bind:checked={t.enabled} aria-label="Activé" />
          activé
        </label>
        <input
          type="text"
          class="mono name"
          aria-label="Nom du déclencheur"
          placeholder="nom"
          bind:value={t.name}
          class:bad={bad(`${p}.name`)}
          data-path="{p}.name"
        />
        <select aria-label="Type" bind:value={t.type} class:bad={bad(`${p}.type`)} data-path="{p}.type">
          <option value="event">événement</option>
          <option value="schedule">planification (cron)</option>
        </select>
        <span class="grow"></span>
        {#if n}<span class="count">{n} problème{n > 1 ? 's' : ''}</span>{/if}
        {#if !readonly}
          <RowTools
            index={j}
            count={triggers.length}
            label="le déclencheur"
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
          <label for="{p}-cron">Planification <span class="opt">(cron à 5 champs, UTC)</span></label>
          <input
            id="{p}-cron"
            type="text"
            class="mono"
            placeholder="0 6 * * 1-5"
            bind:value={t.schedule}
            class:bad={bad(`${p}.schedule`)}
            data-path="{p}.schedule"
          />
          <p class="hint">minute heure jour-du-mois mois jour-de-semaine — ex. <code>0 6 * * 1-5</code> : 6 h UTC du lundi au vendredi ; <code>*/15 * * * *</code> : toutes les 15 min.</p>
        </div>
      {:else}
        <div class="grid">
          <div class="field">
            <label for="{p}-event">Événement</label>
            <select id="{p}-event" bind:value={t.event} class:bad={bad(`${p}.event`)} data-path="{p}.event">
              {#if t.event && !(TRIGGER_EVENTS as readonly string[]).includes(t.event)}<option value={t.event}>{t.event} (inconnu)</option>{/if}
              {#each TRIGGER_EVENTS as e (e)}<option value={e}>{EVENT_LABELS[e]}</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="{p}-filter">Filtre <span class="opt">(CEL, facultatif)</span></label>
          <CodeEditor
            id="{p}-filter"
            bind:value={t.filter}
            language="cel"
            lineNumbers={false}
            {readonly}
            label="Filtre CEL"
            minHeight="1.9rem"
            maxHeight="8rem"
            placeholder={'event.change.methodology == "impact-analysis"'}
            bad={bad(`${p}.filter`)}
            path="{p}.filter"
          />
          <p class="hint">
            Variables : <code>event.type</code>, <code>event.change.{'{'}id, title, status, methodology, goal, baseline{'}'}</code>,
            <code>event.process.{'{'}id, agent, goal, status{'}'}</code>.
          </p>
        </div>
      {/if}
      <div class="grid">
        <div class="field">
          <label for="{p}-goal">Objectif</label>
          <select id="{p}-goal" bind:value={t.goal} class:bad={bad(`${p}.goal`)} data-path="{p}.goal">
            <option value="">— unique objectif de l'agent, ou boucle d'intention —</option>
            {#if t.goal && !goals.includes(t.goal)}<option value={t.goal}>{t.goal} (inconnu)</option>{/if}
            {#each goals as g (g)}<option value={g}>{g}</option>{/each}
          </select>
        </div>
        <div class="field">
          <label for="{p}-target">Cible</label>
          <select id="{p}-target" bind:value={t.target} class:bad={bad(`${p}.target`)} data-path="{p}.target">
            {#each TRIGGER_TARGETS as x (x)}<option value={x}>{TARGET_LABELS[x]}</option>{/each}
          </select>
        </div>
        <div class="field">
          <label for="{p}-roles">Rôles <span class="opt">(séparés par des virgules)</span></label>
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
        <label for="{p}-intent">Intention transmise au processus</label>
        <textarea id="{p}-intent" rows="2" bind:value={t.intent} class:bad={bad(`${p}.intent`)} data-path="{p}.intent"></textarea>
      </div>
    </div>
  {:else}
    <p class="empty">Aucun déclencheur : l'agent ne s'exécute qu'à la demande.</p>
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
