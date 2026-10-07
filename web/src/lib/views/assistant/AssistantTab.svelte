<script lang="ts">
  // "Assistant" tab (same conversation as the floating panel).
  import type { Tab } from '../../shell/types';
  import Assistant from './Assistant.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import { assistant, newConversation } from '../../stores/assistant.svelte';
  import { assistantEnabled } from '../../assistant/enabled';

  let { tab }: { tab: Tab } = $props();

  provideActions(
    () => tab.id,
    () => [
      {
        id: 'new',
        label: 'New conversation',
        icon: 'plus',
        disabled: !assistantEnabled() || !assistant.messages.length,
        run: newConversation,
      },
    ],
  );
</script>

<Assistant mode="tab" />
