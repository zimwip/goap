<script lang="ts">
  // "Assistant" tab (same conversation as the side tool).
  import type { Tab } from '../../shell/types';
  import Assistant from './Assistant.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import { newConversation, conversation } from '../../stores/assistant.svelte';

  let { tab }: { tab: Tab } = $props();

  provideActions(
    () => tab.id,
    () => [
      {
        id: 'new',
        label: 'New conversation',
        icon: 'plus',
        disabled: !conversation.threads.length,
        run: () => {
          if (confirm('Start a new conversation? The current history will be erased.')) newConversation();
        },
      },
    ],
  );
</script>

<Assistant mode="tab" />
