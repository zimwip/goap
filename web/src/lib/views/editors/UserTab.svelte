<script lang="ts">
  // User tab (ADR 0039/0020): an organisation@User node — created automatically the first time a subject
  // is seen (internal/graphsvc.EnsureUser), not only by an administrator's hand. User extends OrgUnit (the
  // smallest organisational unit is a person), so it is assignable to a project the same way a team is
  // (ADR 0039): its Assignments pane is the meeting point with project.
  import type { Tab, ToolbarAction } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import AssignmentsPane from '../../components/AssignmentsPane.svelte';
  import { errorMessage, logout } from '../../api';
  import { headGraph, findNode, applyOnMain, updateNodeItem, currentLink, moveNodeItem, refOf, type HeadGraph } from '../../graphEdit';
  import { notify, provideActions } from '../../shell/workbench.svelte';
  import { USER_TYPE, ORG_UNIT_TYPE, MEMBER_OF } from '../../orgTypes';
  import { session, me } from '../../stores/session.svelte';
  import { authState } from '../../stores/auth.svelte';

  let { tab }: { tab: Tab } = $props();

  const NS = 'organisation';
  const key = $derived(tab.params.key ?? '');

  let head = $state<HeadGraph>();
  let loading = $state(false);
  let error = $state('');
  let pane = $state(tab.params.pane === 'assignments' ? 'assignments' : 'overview');

  const user = $derived(head ? findNode(head, NS, USER_TYPE, key) : undefined);
  const orgLink = $derived(head && user ? currentLink(head, user, MEMBER_OF) : undefined);
  const org = $derived(head && orgLink ? head.nodes.find((n) => n.id === orgLink.to?.id) : undefined);
  const orgUnits = $derived(head ? head.nodes.filter((n) => n.type === ORG_UNIT_TYPE) : []);

  async function load() {
    loading = true;
    try {
      head = await headGraph(NS);
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void key;
    void load();
  });

  const isSelf = $derived(!!user && user.props?.['subject'] === me());
  // Logout (ADR 0040): stateless HS256 has nothing to revoke server-side, so this is a client-side sign-out;
  // hidden once the deployment gains a real SSO mode (OIDC/OAuth), where signing out goes through the IdP.
  const canLogout = $derived(isSelf && session.hasToken && authState.mode !== 'oidc');

  async function doLogout() {
    await logout();
    notify('Signed out.', 'ok');
  }

  provideActions(
    () => tab.id,
    () => {
      const actions: ToolbarAction[] = [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: load }];
      if (canLogout) actions.push({ id: 'logout', label: 'Log out', icon: 'logout', run: doLogout });
      return actions;
    },
  );

  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview' },
    { id: 'assignments', label: 'Assignments' },
  ]);

  let editing = $state(false);
  let fDisplayName = $state('');
  let fEmail = $state('');
  let fLocale = $state('');
  let fRoles = $state('');
  let saving = $state(false);

  let moving = $state(false);
  let fOrg = $state('');
  let movingBusy = $state(false);

  function startMove() {
    fOrg = org?.key ?? '';
    moving = true;
  }

  async function move() {
    if (!user || !head || !fOrg || fOrg === org?.key) {
      moving = false;
      return;
    }
    const target = head.nodes.find((n) => n.type === ORG_UNIT_TYPE && n.key === fOrg);
    if (!target) return;
    movingBusy = true;
    error = '';
    try {
      await applyOnMain(NS, `Move ${key}`, `Move ${key} to ${fOrg}`, head.baselineId, [moveNodeItem(user, MEMBER_OF, orgLink, refOf(target))]);
      notify(`${key} moved to ${fOrg}.`, 'ok');
      moving = false;
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      movingBusy = false;
    }
  }

  function startEdit() {
    fDisplayName = typeof user?.props?.['displayName'] === 'string' ? (user!.props!['displayName'] as string) : '';
    fEmail = typeof user?.props?.['email'] === 'string' ? (user!.props!['email'] as string) : '';
    fLocale = typeof user?.props?.['locale'] === 'string' ? (user!.props!['locale'] as string) : '';
    fRoles = Array.isArray(user?.props?.['roles']) ? (user!.props!['roles'] as string[]).join(', ') : '';
    editing = true;
  }

  async function save() {
    if (!user || !head) return;
    saving = true;
    error = '';
    try {
      const roles = fRoles
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
      await applyOnMain(NS, `User ${key}`, `Update user ${key}`, head.baselineId, [
        updateNodeItem(user, { displayName: fDisplayName.trim() || null, email: fEmail.trim() || null, locale: fLocale.trim() || null, roles: roles.length ? roles : null }),
      ]);
      notify(`User ${key} updated.`, 'ok');
      editing = false;
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="editor-page">
  {#if error}<div class="alert">{error}</div>{/if}
  {#if !user && !loading}
    <p class="empty">User {key} is not on the graph.</p>
  {:else if user}
    <EditorPanes {panes} bind:active={pane} label="User sections">
      {#snippet children(active)}
        {#if active === 'overview'}
          <section class="card">
            <div class="head"><Icon name="user" size={18} /><h2>{String(user.props?.['displayName'] ?? user.props?.['subject'] ?? key)}</h2></div>
            {#if !editing}
              <dl class="kv">
                <dt>Key</dt><dd><code>{key}</code></dd>
                <dt>Subject</dt><dd><code>{user.props?.['subject'] ?? ''}</code></dd>
                {#if user.props?.['email']}<dt>Email</dt><dd>{user.props['email']}</dd>{/if}
                {#if user.props?.['locale']}<dt>Locale</dt><dd>{user.props['locale']}</dd>{/if}
                <dt>Organisation</dt>
                <dd>
                  {#if !moving}
                    <code>{org?.props?.['name'] ?? org?.key ?? 'none'}</code>
                    <button type="button" class="small ghost" onclick={startMove}>Move to…</button>
                  {:else}
                    <select bind:value={fOrg} disabled={movingBusy}>
                      {#each orgUnits as o (o.id)}
                        <option value={o.key}>{String(o.props?.['name'] ?? o.key)}</option>
                      {/each}
                    </select>
                    <button type="button" class="small primary" disabled={movingBusy} onclick={move}>Move</button>
                    <button type="button" class="small" disabled={movingBusy} onclick={() => (moving = false)}>Cancel</button>
                  {/if}
                </dd>
                <dt>Global roles</dt>
                <dd>
                  {#if Array.isArray(user.props?.['roles']) && (user.props['roles'] as string[]).length}
                    <code>{(user.props['roles'] as string[]).join(', ')}</code>
                  {:else}<span class="muted">none</span>{/if}
                </dd>
              </dl>
              <p class="hint">
                Global roles hold everywhere (or in the unit they are scoped to, "role@UNIT"). The Assignments pane grants roles for one project only (ADR 0039).
              </p>
              <button type="button" class="small" onclick={startEdit}>Edit</button>
            {:else}
              <div class="grid">
                <div class="field">
                  <label for="usr-name">Display name</label>
                  <input id="usr-name" bind:value={fDisplayName} />
                </div>
                <div class="field">
                  <label for="usr-email">Email</label>
                  <input id="usr-email" bind:value={fEmail} />
                </div>
                <div class="field">
                  <label for="usr-locale">Locale</label>
                  <input id="usr-locale" bind:value={fLocale} />
                </div>
                <div class="field">
                  <label for="usr-roles">Global roles</label>
                  <input id="usr-roles" placeholder="admin, methodologist…" bind:value={fRoles} />
                </div>
              </div>
              <div class="row">
                <button type="button" class="small primary" disabled={saving} onclick={save}>Save</button>
                <button type="button" class="small" onclick={() => (editing = false)}>Cancel</button>
              </div>
            {/if}
          </section>
        {:else if head}
          <AssignmentsPane {head} fixedOrg={key} autoOpen={tab.params.newAssignment === '1'} onChanged={load} />
        {/if}
      {/snippet}
    </EditorPanes>
  {/if}
</div>

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.6rem;
  }
  .head h2 {
    margin: 0;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.25rem 1rem;
  }
  .kv dt {
    color: var(--muted);
  }
  .kv dd {
    margin: 0;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
</style>
