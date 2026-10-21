<script lang="ts">
  // "Assistant" tab (same conversation as the floating panel).
  import type { Tab } from '../../shell/types';
  import Assistant from './Assistant.svelte';

  // the conversation holds its own actions (a new conversation)
  let { tab: _tab }: { tab: Tab } = $props();
</script>

<Assistant mode="tab" />
