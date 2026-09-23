<script lang="ts">
  // Onglet « Assistant » (même conversation que l'outil latéral).
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
        label: 'Nouvelle conversation',
        icon: 'plus',
        disabled: !conversation.threads.length,
        run: () => {
          if (confirm('Commencer une nouvelle conversation ? L’historique actuel sera effacé.')) newConversation();
        },
      },
    ],
  );
</script>

<Assistant mode="tab" />
