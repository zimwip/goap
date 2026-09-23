<script lang="ts">
  // Onglet « Politiques » : administration des politiques ABAC (évaluées par Casbin).
  import { iam, errorMessage, type Policy } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { provideActions } from '../../shell/workbench.svelte';

  let { tab }: { tab: Tab } = $props();

  let policies = $state<Policy[]>([]);
  let loading = $state(true);
  let error = $state('');
  let removing = $state(-1);

  async function load() {
    loading = true;
    error = '';
    try {
      policies = (await iam.listPolicies()).policies ?? [];
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  async function remove(p: Policy, i: number) {
    if (!confirm(`Supprimer la politique « ${p.effect} ${p.resource}/${p.action} » ?\n\n${p.rule}`)) return;
    removing = i;
    error = '';
    try {
      await iam.removePolicy(p);
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      removing = -1;
    }
  }

  // --- ajout --------------------------------------------------------------------

  let rule = $state('');
  let resource = $state('');
  let action = $state('');
  let policyEffect = $state<'allow' | 'deny'>('allow');
  let adding = $state(false);
  let addError = $state('');

  const canAdd = $derived(!!rule.trim() && !!resource.trim() && !!action.trim() && !adding);

  async function add(e: SubmitEvent) {
    e.preventDefault();
    if (!canAdd) return;
    adding = true;
    addError = '';
    try {
      await iam.addPolicy({ rule: rule.trim(), resource: resource.trim(), action: action.trim(), effect: policyEffect });
      rule = '';
      resource = '';
      action = '';
      policyEffect = 'allow';
      await load();
    } catch (err) {
      addError = errorMessage(err);
    } finally {
      adding = false;
    }
  }

  const EXAMPLES: { title: string; policy: Required<Policy> }[] = [
    {
      title: 'Principe des quatre yeux : un approbateur de la même organisation, autre que l’auteur, applique le changement',
      policy: {
        rule: 'hasRole(r.sub, "approver") && r.sub.Org == r.obj.Org && r.sub.Subject != r.obj.Owner',
        resource: 'change',
        action: 'apply',
        effect: 'allow',
      },
    },
    {
      title: 'Les utilisateurs anonymes n’ont aucun accès au registre des méthodologies',
      policy: {
        rule: 'isAnonymous(r.sub)',
        resource: 'methodology',
        action: '*',
        effect: 'deny',
      },
    },
  ];

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Actualiser', icon: 'refresh', disabled: loading, run: load }],
  );

  function useExample(p: Required<Policy>) {
    rule = p.rule;
    resource = p.resource;
    action = p.action;
    policyEffect = p.effect === 'deny' ? 'deny' : 'allow';
    document.getElementById('pol-rule')?.focus();
  }
</script>

<div class="editor-page">
<div class="editor-head">
  <Icon name="shield" size={18} />
  <h2>Politiques d'accès</h2>
</div>

<p class="hint intro">
  Le contrôle d'accès est fondé sur les attributs (ABAC). Une requête est autorisée si au moins une politique
  <em>autoriser</em> correspond et qu'aucune politique <em>refuser</em> ne correspond (le refus l'emporte).
</p>

{#if error}<div class="alert">{error}</div>{/if}

<section class="card">
  <h3>Politiques</h3>
  {#if loading && policies.length === 0}
    <p class="empty">Chargement…</p>
  {:else if policies.length === 0 && !error}
    <p class="empty">Aucune politique définie.</p>
  {:else if policies.length}
    <div class="scroll">
      <table>
        <thead>
          <tr>
            <th>Règle</th>
            <th>Ressource</th>
            <th>Action</th>
            <th>Effet</th>
            <th><span class="sr-only">Supprimer</span></th>
          </tr>
        </thead>
        <tbody>
          {#each policies as p, i (i)}
            <tr>
              <td class="rule"><code>{p.rule}</code></td>
              <td><code>{p.resource}</code></td>
              <td><code>{p.action}</code></td>
              <td><StatusBadge status={p.effect} /></td>
              <td class="actions">
                <button class="small danger" disabled={removing >= 0} onclick={() => remove(p, i)}>
                  {removing === i ? '…' : 'Supprimer'}
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<div class="cols">
  <form class="card" onsubmit={add}>
    <h3>Ajouter une politique</h3>
    <div class="field">
      <label for="pol-rule">Règle</label>
      <textarea
        id="pol-rule"
        class="mono"
        rows="3"
        spellcheck="false"
        bind:value={rule}
        placeholder={'hasRole(r.sub, "editor") && r.sub.Org == r.obj.Org'}
      ></textarea>
    </div>
    <div class="pgrid">
      <div class="field">
        <label for="pol-res">Ressource</label>
        <input id="pol-res" type="text" class="mono" bind:value={resource} placeholder="change ou *" />
      </div>
      <div class="field">
        <label for="pol-act">Action</label>
        <input id="pol-act" type="text" class="mono" bind:value={action} placeholder="apply ou *" />
      </div>
      <div class="field">
        <label for="pol-eff">Effet</label>
        <select id="pol-eff" bind:value={policyEffect}>
          <option value="allow">autoriser (allow)</option>
          <option value="deny">refuser (deny)</option>
        </select>
      </div>
    </div>
    {#if addError}<div class="alert">{addError}</div>{/if}
    <button class="primary" type="submit" disabled={!canAdd}>{adding ? 'Ajout…' : 'Ajouter'}</button>
  </form>

  <section class="card help">
    <h3>Écrire une règle</h3>
    <p>
      La règle est une expression booléenne évaluée pour chaque requête. La ressource et l'action de la
      politique (ou <code>*</code>) filtrent les requêtes concernées.
    </p>
    <dl>
      <dt><code>r.sub</code></dt>
      <dd>le demandeur : <code>Subject</code> (identifiant), <code>Org</code>, <code>Roles</code></dd>
      <dt><code>r.obj</code></dt>
      <dd>
        la ressource : <code>Type</code>, <code>ID</code>, <code>Org</code>, <code>Owner</code> (auteur),
        <code>Name</code>
      </dd>
      <dt><code>r.act</code></dt>
      <dd>l'action demandée (ex. <code>apply</code>)</dd>
    </dl>
    <p>Fonctions : <code>hasRole(r.sub, "x")</code>, <code>hasAnyRole(r.sub, "a", "b")</code>, <code>isAnonymous(r.sub)</code>.</p>
    <h4>Exemples</h4>
    {#each EXAMPLES as ex (ex.title)}
      <div class="example">
        <p>{ex.title} :</p>
        <pre>{ex.policy.rule}</pre>
        <div class="row">
          <span class="hint grow">
            <code>{ex.policy.resource}</code> / <code>{ex.policy.action}</code> →
            <StatusBadge status={ex.policy.effect} />
          </span>
          <button type="button" class="small" onclick={() => useExample(ex.policy)}>Utiliser</button>
        </div>
      </div>
    {/each}
  </section>
</div>
</div>

<style>
  .intro {
    max-width: 60rem;
  }
  .scroll {
    overflow-x: auto;
  }
  .rule {
    min-width: 16rem;
  }
  .rule code {
    white-space: pre-wrap;
    word-break: break-word;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: 0 1rem;
    align-items: start;
  }
  .pgrid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
    gap: 0 0.85rem;
  }
  .help {
    font-size: 0.9rem;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.3rem 0.8rem;
    margin: 0 0 0.75rem;
  }
  dt {
    font-weight: 600;
  }
  dd {
    margin: 0;
  }
  .example {
    margin-bottom: 0.9rem;
  }
  .example p {
    margin-bottom: 0.3rem;
  }
  .example pre {
    margin-bottom: 0.35rem;
  }
</style>
