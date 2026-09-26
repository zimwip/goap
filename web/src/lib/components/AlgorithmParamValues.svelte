<script lang="ts">
  // Values form generated from the declared parameters of an algorithm: one control per
  // parameter type, typed values written into `values` (unset parameters are removed, so
  // the default of the algorithm applies).
  import type { ParamForm } from '../algorithmForm';

  let {
    params,
    values = $bindable(),
    readonly = false,
    idPrefix = 'pv',
    path,
  }: {
    params: ParamForm[];
    values: Record<string, unknown>;
    readonly?: boolean;
    idPrefix?: string;
    /** path of the values in validation issues */
    path?: string;
  } = $props();

  // text of json parameters being typed (invalid JSON is not stored)
  let jsonText = $state<Record<string, string>>({});
  let jsonError = $state<Record<string, string>>({});

  const enumValues = (p: ParamForm) =>
    p.values
      .split(',')
      .map((x) => x.trim())
      .filter(Boolean);

  function set(p: ParamForm, v: unknown) {
    if (v === undefined || v === '') delete values[p.name];
    else values[p.name] = v;
  }

  function regexError(v: unknown): string {
    if (typeof v !== 'string' || !v) return '';
    try {
      new RegExp(v);
      return '';
    } catch (e) {
      return (e as Error).message;
    }
  }

  function typeJson(p: ParamForm, text: string) {
    jsonText[p.name] = text;
    if (!text.trim()) {
      jsonError[p.name] = '';
      set(p, undefined);
      return;
    }
    try {
      set(p, JSON.parse(text) as unknown);
      jsonError[p.name] = '';
    } catch (e) {
      jsonError[p.name] = (e as Error).message;
    }
  }

  const strOf = (v: unknown) => (v === undefined || v === null ? '' : String(v));
</script>

<div class="values" data-path={path}>
  {#each params as p, i (p.name + i)}
    {@const id = `${idPrefix}-${i}`}
    {@const v = values[p.name]}
    <div class="row">
      <label for={id}>
        <span class="mono">{p.name || '(unnamed)'}</span>
        <span class="type">{p.type}</span>{#if p.required}<span class="req" title="Required">*</span>{/if}
      </label>
      <div class="ctl">
        {#if p.type === 'number'}
          <input {id} type="number" step="any" value={strOf(v)} disabled={readonly} placeholder={p.defaultValue} oninput={(e) => set(p, e.currentTarget.value === '' ? undefined : Number(e.currentTarget.value))} />
        {:else if p.type === 'boolean'}
          <select {id} disabled={readonly} value={v === true ? 'true' : v === false ? 'false' : ''} onchange={(e) => set(p, e.currentTarget.value === '' ? undefined : e.currentTarget.value === 'true')}>
            <option value="">{p.defaultValue ? `default (${p.defaultValue})` : '— unset —'}</option>
            <option value="true">true</option>
            <option value="false">false</option>
          </select>
        {:else if p.type === 'enum'}
          <select {id} disabled={readonly} value={strOf(v)} onchange={(e) => set(p, e.currentTarget.value)}>
            <option value="">{p.defaultValue ? `default (${p.defaultValue})` : '— unset —'}</option>
            {#each enumValues(p) as o (o)}<option value={o}>{o}</option>{/each}
          </select>
        {:else if p.type === 'strings'}
          <input {id} type="text" class="mono" disabled={readonly} placeholder={p.defaultValue || 'a, b, c'} value={Array.isArray(v) ? v.join(', ') : strOf(v)} oninput={(e) => set(p, e.currentTarget.value.trim() ? e.currentTarget.value.split(',').map((x) => x.trim()).filter(Boolean) : undefined)} />
        {:else if p.type === 'json'}
          <textarea {id} class="mono" rows="2" disabled={readonly} placeholder={p.defaultValue || '{"any": "json"}'} value={jsonText[p.name] ?? (v === undefined ? '' : JSON.stringify(v))} oninput={(e) => typeJson(p, e.currentTarget.value)}></textarea>
          {#if jsonError[p.name]}<span class="err">{jsonError[p.name]}</span>{/if}
        {:else}
          <input {id} type="text" class:mono={p.type === 'regex'} disabled={readonly} placeholder={p.defaultValue} value={strOf(v)} oninput={(e) => set(p, e.currentTarget.value)} />
          {#if p.type === 'regex' && regexError(v)}<span class="err">{regexError(v)}</span>{/if}
        {/if}
        {#if p.description}<span class="hint">{p.description}</span>{/if}
      </div>
    </div>
  {:else}
    <p class="empty">This algorithm has no parameters.</p>
  {/each}
</div>

<style>
  .values {
    display: grid;
    gap: 0.5rem;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(8rem, 12rem) 1fr;
    gap: 0.6rem;
    align-items: start;
  }
  label {
    display: flex;
    gap: 0.4rem;
    align-items: baseline;
    font-weight: 500;
  }
  .type {
    font-size: 0.75rem;
    color: var(--muted);
  }
  .req {
    color: var(--danger);
  }
  .ctl {
    display: grid;
    gap: 0.15rem;
  }
  .err {
    color: var(--danger);
    font-size: 0.82rem;
  }
  @media (max-width: 700px) {
    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
