<script lang="ts">
  // "Preferences" section of the settings modal: identity, access token, theme, voice input and dashboard
  // defaults — everyone's own settings. They are saved as they change (the preferences service, ADR 0038): no
  // change, no Save button. The access token is kept in the browser: it is needed before any call.
  import { getToken, setToken } from '../../api';
  import { session, refreshIdentity, hasAnyRole } from '../../stores/session.svelte';
  import { prefs, editPrefs, type Prefs } from '../../stores/preferences.svelte';
  import { voiceSupported } from '../../voice/recorder';

  const principal = $derived(session.principal);
  const hasToken = $derived(session.hasToken);
  const supported = voiceSupported();
  const isAdmin = $derived(hasAnyRole('admin'));
  const set = <K extends keyof Prefs>(k: K, v: Prefs[K]) => void editPrefs({ [k]: v } as Partial<Prefs>);

  let tokenDraft = $state(getToken() ?? '');

  function saveToken(e: SubmitEvent) {
    e.preventDefault();
    setToken(tokenDraft.trim() || null);
    void refreshIdentity();
  }

  function clearToken() {
    setToken(null);
    tokenDraft = '';
    void refreshIdentity();
  }
</script>

<div class="prefs">
  {#if prefs.error}<div class="alert">{prefs.error}</div>{/if}
  <p class="hint">Your preferences are saved as you change them.</p>
  <section class="card">
    <h3>Identity</h3>
    <p class="hint">
      {#if principal?.subject}
        Signed in as <strong>{principal.subject}</strong>{principal.org ? ` · ${principal.org}` : ''}
        {#if principal.roles?.length}<br />Roles: {principal.roles.join(', ')}{/if}
      {:else}
        {session.error || 'No identity.'}
      {/if}
    </p>
    <form onsubmit={saveToken} class="row">
      <div class="field grow">
        <label for="pref-token">Access token (Bearer)</label>
        <input id="pref-token" type="password" bind:value={tokenDraft} autocomplete="off" placeholder="token…" />
      </div>
      <button class="small primary" type="submit">Save</button>
      {#if hasToken}<button class="small" type="button" onclick={clearToken}>Remove</button>{/if}
    </form>
  </section>

  <section class="card">
    <h3>Appearance</h3>
    <div class="field">
      <label for="pref-theme">Theme</label>
      <select id="pref-theme" value={prefs.values.theme} onchange={(e) => set('theme', e.currentTarget.value as Prefs['theme'])}>
        <option value="auto">System</option>
        <option value="light">Light</option>
        <option value="dark">Dark</option>
      </select>
    </div>
  </section>

  <section class="card">
    <h3>Token usage</h3>
    <div class="row">
      <div class="field">
        <label for="pref-usage-period">Default period</label>
        <select id="pref-usage-period" value={prefs.values.usagePeriod} onchange={(e) => set('usagePeriod', e.currentTarget.value)}>
          <option value="24h">Last 24 hours</option>
          <option value="7d">Last 7 days</option>
          <option value="30d">Last 30 days</option>
          <option value="all">All history</option>
        </select>
      </div>
      {#if isAdmin}
        <div class="field">
          <label for="pref-usage-scope">Default scope</label>
          <select id="pref-usage-scope" value={prefs.values.usageScope} onchange={(e) => set('usageScope', e.currentTarget.value as Prefs['usageScope'])}>
            <option value="mine">My consumption</option>
            <option value="platform">Whole platform</option>
          </select>
        </div>
      {/if}
    </div>
  </section>

  {#if supported}
    <section class="card">
      <h3>Voice input</h3>
      <label class="check">
        <input type="checkbox" checked={prefs.values.voiceEnabled} onchange={(e) => set('voiceEnabled', e.currentTarget.checked)} />
        Enable push-to-talk
      </label>
      {#if prefs.values.voiceEnabled}
        <div class="row">
          <div class="field">
            <label for="pref-voice-model">Model</label>
            <select id="pref-voice-model" value={prefs.values.voiceModel} onchange={(e) => set('voiceModel', e.currentTarget.value as Prefs['voiceModel'])}>
              <option value="tiny">Tiny (faster)</option>
              <option value="base">Base (more accurate)</option>
            </select>
          </div>
          <div class="field">
            <label for="pref-voice-lang">Language</label>
            <select id="pref-voice-lang" value={prefs.values.voiceLanguage} onchange={(e) => set('voiceLanguage', e.currentTarget.value as Prefs['voiceLanguage'])}>
              <option value="auto">Auto-detect</option>
              <option value="en">English</option>
              <option value="fr">Français</option>
            </select>
          </div>
        </div>
      {/if}
    </section>
  {/if}
</div>

<style>
  .prefs {
    display: grid;
    gap: 0.8rem;
  }
  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95em;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: flex-end;
    flex-wrap: wrap;
  }
  .grow {
    flex: 1;
    min-width: 12rem;
  }

  .check {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-weight: 500;
    margin-bottom: 0.5rem;
  }
  .check input {
    width: auto;
  }
</style>
