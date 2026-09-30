<script lang="ts">
  // Sign-in / sign-up screen (ADR 0040): shown instead of the shell when the gateway has no external
  // identity provider (AuthMode "local") and there is no token yet. Backed by /auth/register and
  // /auth/login (internal/credsvc), the same JWT the hs256/dev-token flow already produces.
  import { register, login, errorMessage } from '../api';

  let mode = $state<'login' | 'register'>('login');
  let subject = $state('');
  let password = $state('');
  let busy = $state(false);
  let error = $state('');

  async function submit(e: Event) {
    e.preventDefault();
    if (!subject.trim() || !password) return;
    busy = true;
    error = '';
    try {
      await (mode === 'login' ? login(subject.trim(), password) : register(subject.trim(), password));
      // a successful call sets the token; the app re-renders the shell once session picks it up
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="signin">
  <form class="card" onsubmit={submit}>
    <h1>GOAP</h1>
    <div class="tabs" role="tablist">
      <button type="button" role="tab" aria-selected={mode === 'login'} class:active={mode === 'login'} onclick={() => (mode = 'login')}>Sign in</button>
      <button type="button" role="tab" aria-selected={mode === 'register'} class:active={mode === 'register'} onclick={() => (mode = 'register')}>
        Create account
      </button>
    </div>
    {#if error}<p class="alert">{error}</p>{/if}
    <div class="field">
      <label for="signin-subject">Subject</label>
      <input id="signin-subject" bind:value={subject} autocomplete="username" required />
    </div>
    <div class="field">
      <label for="signin-password">Password</label>
      <input id="signin-password" type="password" bind:value={password} autocomplete={mode === 'login' ? 'current-password' : 'new-password'} minlength="8" required />
    </div>
    <button type="submit" class="primary" disabled={busy}>{mode === 'login' ? 'Sign in' : 'Create account'}</button>
    {#if mode === 'register'}<p class="hint">The first account created becomes the administrator.</p>{/if}
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
