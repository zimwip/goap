<script lang="ts">
  // Écran « Méthodologies » : liste des versions (#methodologies) ou éditeur
  // (#methodologies/<nom>/<version>, #methodologies/new).
  import { nav } from '../nav.svelte';
  import MethodologyList from './MethodologyList.svelte';
  import MethodologyEditor from './MethodologyEditor.svelte';
</script>

{#if nav.id}
  {#key `${nav.id}/${nav.sub}`}
    <MethodologyEditor name={nav.id} version={nav.sub} />
  {/key}
{:else}
  <MethodologyList />
{/if}
