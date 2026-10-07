<script lang="ts">
  // "Assistant" side tool (activity bar): the same conversation as the assistant tab and the floating panel.
  import Assistant from './Assistant.svelte';
  import { openTab } from '../../shell/tabs.svelte';
</script>

<Assistant mode="panel" onexpand={() => openTab({ kind: 'assistant', params: {} }, { pin: true })} />
