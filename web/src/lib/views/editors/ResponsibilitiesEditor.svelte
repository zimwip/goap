<script lang="ts">
  // RACI roles of a step or a method (ADR 0035 §2): the responsible role performs its human tasks, the accountable
  // role may approve its gates (never on its own change); consulted and informed are involved. The roles are those
  // the methodology declares; the organisation assigns them to users per unit.
  import type { ResponsibilitiesForm } from '../../methodologyForm';

  let {
    roles = $bindable(),
    declared,
    path,
    bad,
    hint = '',
  }: {
    roles: ResponsibilitiesForm;
    /** the roles the methodology declares */
    declared: string[];
    path: string;
    bad: (path: string, exact?: boolean) => boolean;
    hint?: string;
  } = $props();

  const listId = $derived(`roles-${path.replace(/[^a-z0-9]/gi, '-')}`);
</script>

<div class="resp" data-path={path}>
  <div class="label">Roles <span class="opt">{hint}</span></div>
  <div class="grid4">
    {#each [['responsible', 'Responsible'], ['accountable', 'Accountable']] as [key, label] (key)}
      <label>
        <span class="k">{label}</span>
        <select bind:value={roles[key as 'responsible' | 'accountable']} class:bad={bad(`${path}.${key}`)} data-path="{path}.{key}">
          <option value="">—</option>
          {#if roles[key as 'responsible' | 'accountable'] && !declared.includes(roles[key as 'responsible' | 'accountable'])}
            <option value={roles[key as 'responsible' | 'accountable']}>{roles[key as 'responsible' | 'accountable']} (undeclared)</option>
          {/if}
          {#each declared as r (r)}<option value={r}>{r}</option>{/each}
        </select>
      </label>
    {/each}
    <label>
      <span class="k">Consulted</span>
      <input type="text" list={listId} bind:value={roles.consulted} class:bad={bad(`${path}.consulted`)} data-path="{path}.consulted" placeholder="role, role" />
    </label>
    <label>
      <span class="k">Informed</span>
      <input type="text" list={listId} bind:value={roles.informed} class:bad={bad(`${path}.informed`)} data-path="{path}.informed" placeholder="role, role" />
    </label>
  </div>
  <datalist id={listId}>{#each declared as r (r)}<option value={r}></option>{/each}</datalist>
</div>

<style>
  .resp {
    margin: 6px 0;
  }
  .label {
    font-weight: 600;
    margin-bottom: 4px;
  }
  .grid4 {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 6px;
  }
  @media (max-width: 800px) {
    .grid4 {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  .k {
    display: block;
    font-size: 0.85em;
    color: var(--muted);
  }
  select,
  input {
    width: 100%;
  }
</style>
