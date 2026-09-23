<script lang="ts">
  // Racine : coquille de l'IDE, flux d'événements global, validation de fond
  // du brouillon actif et garde de sortie.
  import './lib/views';
  import './lib/stores/notifications.svelte';
  import Shell from './lib/shell/Shell.svelte';
  import Welcome from './lib/views/Welcome.svelte';
  import { startLive, refreshProcesses } from './lib/stores/live.svelte';
  import { anyDirty } from './lib/stores/drafts.svelte';
  import { activeDraft } from './lib/views/bottom/activeDraft';

  $effect(() => {
    const stop = startLive();
    void refreshProcesses();
    return stop;
  });

  // Validation automatique du brouillon actif, 800 ms après la dernière modification.
  $effect(() => {
    const d = activeDraft();
    if (!d || d.readonly || d.isNew || d.loading) return;
    const at = d.current;
    if (d.validatedAt === at) return;
    const timer = setTimeout(() => void d.validate(true), 800);
    return () => clearTimeout(timer);
  });

  $effect(() => {
    const onUnload = (e: BeforeUnloadEvent) => {
      if (!anyDirty()) return;
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', onUnload);
    return () => window.removeEventListener('beforeunload', onUnload);
  });
</script>

<Shell welcome={Welcome} />
