<script lang="ts">
  // "People & Organisation" tool: one nav entry, sub-navigated into Organisation (OrgUnit hierarchy),
  // Projects (ProjectUnit hierarchy, ADR 0039) and Users (organisation@User nodes). Assignment (the
  // meeting point of organisation and project) is reached from any of the three, not from here directly.
  import OrganisationExplorer from './OrganisationExplorer.svelte';
  import ProjectsExplorer from './ProjectsExplorer.svelte';
  import UsersExplorer from './UsersExplorer.svelte';

  type Section = 'organisation' | 'projects' | 'users';
  const SECTIONS: { id: Section; label: string }[] = [
    { id: 'organisation', label: 'Organisation' },
    { id: 'projects', label: 'Projects' },
    { id: 'users', label: 'Users' },
  ];

  let section = $state<Section>('organisation');
</script>

<div class="peopleOrg">
  <div class="tabs" role="tablist" aria-label="People and Organisation">
    {#each SECTIONS as s (s.id)}
      <button type="button" role="tab" aria-selected={section === s.id} class:active={section === s.id} onclick={() => (section = s.id)}>{s.label}</button>
    {/each}
  </div>
  <div class="panel">
    {#if section === 'organisation'}
      <OrganisationExplorer />
    {:else if section === 'projects'}
      <ProjectsExplorer />
    {:else}
      <UsersExplorer />
    {/if}
  </div>
</div>

<style>
  .peopleOrg {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .tabs {
    display: flex;
    gap: 2px;
    padding: 0.3rem 0.5rem 0;
    border-bottom: 1px solid var(--border, #8884);
  }
  .tabs button {
    flex: 1;
    border: none;
    border-radius: 0;
    background: none;
    color: var(--muted);
    font: inherit;
    padding: 0.3rem 0.2rem;
    cursor: pointer;
    border-bottom: 2px solid transparent;
  }
  .tabs button:hover {
    color: inherit;
  }
  .tabs button.active {
    color: inherit;
    border-bottom-color: var(--accent);
    font-weight: 600;
  }
  .panel {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
</style>
