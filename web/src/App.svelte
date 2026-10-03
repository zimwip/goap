<script lang="ts">
  // Root: IDE shell, global event stream, background validation
  // of the active draft, and exit guard.
  import './lib/views';
  import './lib/stores/notifications.svelte';
  import Shell from './lib/shell/Shell.svelte';
  import Signin from './lib/shell/Signin.svelte';
  import Welcome from './lib/views/Welcome.svelte';
  import { startLive, refreshProcesses } from './lib/stores/live.svelte';
  import { anyDirty } from './lib/stores/drafts.svelte';
  import { activeDraft } from './lib/views/bottom/activeDraft';
  import { authState, loadAuthConfig } from './lib/stores/auth.svelte';
  import { session } from './lib/stores/session.svelte';

  $effect(() => {
    void loadAuthConfig();
  });

  const needsSignin = $derived(authState.loaded && authState.mode === 'local' && !session.hasToken);

  $effect(() => {
    if (needsSignin) return;
    const stop = startLive();
    void refreshProcesses();
    return stop;
  });

  // Automatic validation of the active draft, 800 ms after the last change.
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

{#if !authState.loaded}
  <!-- deciding whether to show sign-in: avoids a Shell flash for a "local" deployment with no token -->
  {#if authState.unreachable}<p class="wait" role="status">The platform cannot be reached. Retrying…</p>{/if}
{:else if needsSignin}
  <Signin />
{:else}
  <Shell welcome={Welcome} />
{/if}

<style>
  .wait {
    padding: 2rem;
    text-align: center;
    color: var(--muted, gray);
  }
</style>
