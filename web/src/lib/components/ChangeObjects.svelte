<script lang="ts">
  // The default object editor (ADR 0098): the change objects of some types on a change, a table per type laid out from
  // the attributes of the type, a form to add one or edit one (merged into its last version) and the transitions of the
  // lifecycle of its type. Every write is a new version of the change object; the change validates it by its type.
  import { errorMessage, graph, type ChangeObject } from '../api';
  import { orderedAttributes, shownValue } from '../attributes';
  import { asksKey, createWrite, editWrite, keyLabel, objectsOfTypes, transitionsOf } from '../changeObjects';
  import type { ChangeTabProps } from '../changeTabs';
  import { loadTypes, typeCatalog, typeName } from '../stores/types.svelte';
  import NodePropertyForm from './NodePropertyForm.svelte';

  let { changeId, types, objects, readonly, workspace, onchanged }: ChangeTabProps = $props();

  $effect(() => {
    void loadTypes();
  });

  const cat = $derived(typeCatalog.cat);
  /** what the form edits: a new change object of a type, or an existing one */
  let editing = $state<{ type: string; object?: ChangeObject } | undefined>(undefined);
  let newKey = $state('');
  let busy = $state(false);
  let error = $state('');

  async function write(w: Parameters<typeof graph.putChangeObjects>[1][number]): Promise<boolean> {
    busy = true;
    error = '';
    try {
      await graph.putChangeObjects(changeId, [w]);
      onchanged();
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      busy = false;
    }
  }

  async function save(patch: Record<string, unknown>): Promise<boolean> {
    const e = editing;
    if (!e) return false;
    const ok = e.object ? await write(editWrite(e.object, patch)) : await write(createWrite(e.type, patch, newKey.trim(), workspace));
    if (ok) {
      editing = undefined;
      newKey = '';
    }
    return ok;
  }

  const declaredOf = (type: string) => (cat.objectType(type)?.attributes ?? []).map((a) => a.attribute?.name ?? '').filter(Boolean);
  const columns = (type: string) =>
    orderedAttributes(cat.objectType(type)?.attributes ?? [])
      .filter((a) => a.type !== 'json')
      .slice(0, 4);
</script>

{#each types as type (type)}
  {@const info = cat.objectType(type)}
  {@const rows = objectsOfTypes(objects, [type])}
  {@const cols = columns(type)}
  {@const locked = readonly || info?.system === true}
  <section class="card">
    <div class="head">
      <h3 title={type}>{typeName(type)}</h3>
      {#if info?.description}<span class="muted">{info.description}</span>{/if}
      <span class="grow"></span>
      {#if info?.system}<span class="muted" title="Written by the platform once the rules of the methodology hold">recorded by the platform</span>{/if}
      {#if !locked}
        <button type="button" disabled={busy} onclick={() => ((editing = { type }), (newKey = ''))}>New</button>
      {/if}
    </div>
    {#if !info}
      <p class="muted">Unknown change object type {type}.</p>
    {/if}
    {#if editing?.type === type && !editing.object}
      {#if asksKey(info)}
        <label class="key">Key ({info?.key?.ref}) <input bind:value={newKey} placeholder="the {info?.key?.ref} it refers to" /></label>
      {/if}
      <NodePropertyForm props={{}} attributes={info?.attributes ?? []} declared={declaredOf(type)} typeName={typeName(type)} open={info?.additionalProperties === true} {busy} submitLabel="Save"
        onsave={save} oncancel={() => (editing = undefined)} />
    {/if}
    {#if rows.length}
      <table>
        <thead>
          <tr>
            <th>Key</th>
            {#if info?.lifecycle}<th>State</th>{/if}
            {#each cols as c (c.name)}<th>{c.label}</th>{/each}
            <th>Version</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as o (o.type + '/' + o.key + '@' + (o.workspace ?? ''))}
            <tr>
              <td><code>{keyLabel(o)}</code></td>
              {#if info?.lifecycle}<td>{o.state}</td>{/if}
              {#each cols as c (c.name)}<td>{shownValue(c, o.value?.[c.name])}</td>{/each}
              <td title={o.by ? `by ${o.by}` : ''}>v{o.version}</td>
              <td class="actions">
                {#if !locked}
                  <button type="button" class="link" disabled={busy} onclick={() => (editing = { type, object: o })}>Edit</button>
                  {#each transitionsOf(info, o.state) as tr (tr.name)}
                    <button type="button" class="link" disabled={busy} title="to {tr.to}" onclick={() => write(editWrite(o, {}, tr.name))}>{tr.name}</button>
                  {/each}
                {/if}
              </td>
            </tr>
            {#if editing?.object === o}
              <tr>
                <td colspan={cols.length + 4}>
                  <NodePropertyForm props={o.value ?? {}} attributes={info?.attributes ?? []} declared={declaredOf(type)} typeName={typeName(type)}
                    open={info?.additionalProperties === true} {busy} submitLabel="Save" onsave={save} oncancel={() => (editing = undefined)} />
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    {:else if !(editing?.type === type)}
      <p class="muted">None yet.</p>
    {/if}
  </section>
{/each}
{#if error}<p class="error" role="alert">{error}</p>{/if}

<style>
  .head {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }
  .head h3 {
    margin: 0;
  }
  .grow {
    flex: 1;
  }
  .muted {
    color: var(--muted, #888);
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 0.25rem 0.5rem;
    border-bottom: 1px solid var(--border, #ddd);
  }
  .actions {
    white-space: nowrap;
  }
  .key {
    display: block;
    margin-bottom: 0.5rem;
  }
  .error {
    color: var(--danger, #c00);
  }
</style>
