<script lang="ts">
  // Sign-in / sign-up screen (ADR 0040): shown instead of the shell when the gateway has no external
  // identity provider (AuthMode "local") and there is no token yet. Backed by /auth/register and
  // /auth/login (internal/credsvc), the same JWT the hs256/dev-token flow already produces. It tells why the
  // user is here when their session ended (expired, refused) and remembers the last subject that signed in.
  import { register, login, RpcError } from '../api';
  import { authState, lastSubject } from '../stores/auth.svelte';

  const MIN_PASSWORD = 8; // credsvc.MinPasswordLen

  const remembered = lastSubject();
  let mode = $state<'login' | 'register'>('login');
  let subject = $state(remembered);
  let password = $state('');
  let confirm = $state('');
  let showPassword = $state(false);
  let busy = $state(false);
  let error = $state('');

  const mismatch = $derived(mode === 'register' && confirm !== '' && confirm !== password);
  const tooShort = $derived(mode === 'register' && password !== '' && password.length < MIN_PASSWORD);

  function switchMode(m: 'login' | 'register') {
    mode = m;
    error = '';
    confirm = '';
  }

  /** A message for the person, not the HTTP status. */
  function explain(err: unknown): string {
    if (err instanceof RpcError) {
      if (err.status === 0) return 'The platform cannot be reached. Check your connection and try again.';
      if (err.status === 401) return 'Wrong subject or password.';
      if (err.status === 409) return 'An account already exists for this subject: sign in instead.';
      if (err.status === 503) return `The platform is not ready to sign you in yet (${err.message}). Try again in a moment.`;
      if (err.status === 400) return err.message.replace(/^.*?:\s*/, '') || 'Invalid subject or password.';
      return err.message || 'Sign-in failed.';
    }
    return err instanceof Error ? err.message : String(err);
  }

  async function submit(e: Event) {
    e.preventDefault();
    const who = subject.trim();
    if (!who || !password || busy) return;
    if (mode === 'register' && (tooShort || mismatch || !confirm)) {
      error = tooShort ? `The password needs at least ${MIN_PASSWORD} characters.` : 'The two passwords differ.';
      return;
    }
    busy = true;
    error = '';
    try {
      await (mode === 'login' ? login(who, password) : register(who, password));
      // a successful call sets the token, which restarts the page signed in (stores/auth.svelte.ts)
    } catch (err) {
      error = explain(err);
      password = '';
      confirm = '';
    } finally {
      busy = false;
    }
  }
</script>

<div class="signin">
  <form class="card" onsubmit={submit} aria-busy={busy}>
    <h1><span class="logo" aria-hidden="true">◆</span> GOAP</h1>
    {#if authState.notice}<p class="notice" role="status">{authState.notice}</p>{/if}
    <div class="tabs" role="tablist">
      <button type="button" role="tab" aria-selected={mode === 'login'} class:active={mode === 'login'} onclick={() => switchMode('login')}>Sign in</button>
      <button type="button" role="tab" aria-selected={mode === 'register'} class:active={mode === 'register'} onclick={() => switchMode('register')}>
        Create account
      </button>
    </div>
    {#if error}<p class="alert" role="alert">{error}</p>{/if}
    <div class="field">
      <label for="signin-subject">Subject</label>
      <!-- svelte-ignore a11y_autofocus -->
      <input id="signin-subject" bind:value={subject} autocomplete="username" autocapitalize="none" spellcheck="false" required disabled={busy} autofocus={!remembered} />
    </div>
    <div class="field">
      <label for="signin-password">Password</label>
      <div class="pw">
        <!-- svelte-ignore a11y_autofocus -->
        <input
          id="signin-password"
          type={showPassword ? 'text' : 'password'}
          bind:value={password}
          autocomplete={mode === 'login' ? 'current-password' : 'new-password'}
          required
          disabled={busy}
          autofocus={!!remembered}
          aria-invalid={tooShort}
        />
        <button type="button" class="eye" onclick={() => (showPassword = !showPassword)} aria-pressed={showPassword} aria-label={showPassword ? 'Hide the password' : 'Show the password'}>
          {showPassword ? 'Hide' : 'Show'}
        </button>
      </div>
      {#if tooShort}<span class="hint left">At least {MIN_PASSWORD} characters.</span>{/if}
    </div>
    {#if mode === 'register'}
      <div class="field">
        <label for="signin-confirm">Confirm the password</label>
        <input id="signin-confirm" type={showPassword ? 'text' : 'password'} bind:value={confirm} autocomplete="new-password" required disabled={busy} aria-invalid={mismatch} />
        {#if mismatch}<span class="hint left warn">The two passwords differ.</span>{/if}
      </div>
    {/if}
    <button type="submit" class="primary" disabled={busy || !subject.trim() || !password}>
      {busy ? (mode === 'login' ? 'Signing in…' : 'Creating the account…') : mode === 'login' ? 'Sign in' : 'Create account'}
    </button>
    {#if mode === 'register'}<p class="hint">New accounts wait in the unit chosen by the administrator; the first one created becomes the administrator.</p>{/if}
  </form>
</div>

<style>
  .signin {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    background: var(--bg, #f5f5f7);
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 0.7rem;
    width: min(22rem, 90vw);
    padding: 1.5rem;
    border: 1px solid var(--border, #8884);
    border-radius: 10px;
    background: var(--surface, #fff);
    box-shadow: var(--shadow-pop, 0 4px 16px rgba(0, 0, 0, 0.08));
  }
  h1 {
    margin: 0 0 0.25rem;
    text-align: center;
    letter-spacing: 0.05em;
  }
  .tabs {
    display: flex;
    gap: 0.25rem;
    border-bottom: 1px solid var(--border, #8884);
    margin-bottom: 0.25rem;
  }
  .tabs button {
    flex: 1;
    padding: 0.4rem;
    border: none;
    background: none;
    color: var(--muted, #666);
    cursor: pointer;
    border-bottom: 2px solid transparent;
  }
  .tabs button.active {
    color: inherit;
    border-bottom-color: var(--accent, #4a7cff);
    font-weight: 600;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
  label {
    font-size: 0.8rem;
    color: var(--muted, #666);
  }
  input {
    padding: 0.4rem 0.5rem;
    border: 1px solid var(--border, #8884);
    border-radius: 6px;
    background: var(--bg, #fff);
    color: inherit;
    font: inherit;
  }
  button.primary {
    margin-top: 0.3rem;
    padding: 0.5rem;
    border: none;
    border-radius: 6px;
    background: var(--accent, #4a7cff);
    color: #fff;
    font-weight: 600;
    cursor: pointer;
  }
  button.primary:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .logo {
    color: var(--accent, #4a7cff);
  }
  .notice {
    margin: 0;
    padding: 0.45rem 0.6rem;
    border-radius: 6px;
    background: var(--accent-soft, #eef3ff);
    font-size: 0.85rem;
  }
  .pw {
    display: flex;
    gap: 0.3rem;
  }
  .pw input {
    flex: 1;
    min-width: 0;
  }
  .eye {
    padding: 0 0.6rem;
    border: 1px solid var(--border, #8884);
    border-radius: 6px;
    background: none;
    color: var(--muted, #666);
    font-size: 0.8rem;
    cursor: pointer;
  }
  input[aria-invalid='true'] {
    border-color: var(--danger, #c33);
  }
  .hint.left {
    text-align: left;
  }
  .hint.warn {
    color: var(--danger, #c33);
  }
  .alert {
    margin: 0;
    padding: 0.4rem 0.5rem;
    border-radius: 6px;
    background: #fee;
    color: #a00;
    font-size: 0.85rem;
  }
  .hint {
    margin: 0;
    font-size: 0.78rem;
    color: var(--muted, #666);
    text-align: center;
  }
</style>
