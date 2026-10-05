<script lang="ts">
  // Access tab: the ABAC policies (Policy nodes, evaluated by Casbin) and the users (User nodes: profile; their roles are
  // granted by Assignments, ADR 0043, 0047) of the organisation namespace, changed through
  // changes applied on main.
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { errorMessage, type GraphNode, type Policy } from '../../api';
  import { headGraph, applyOnMain, createNodeItem, deleteNodeItem, currentLink, refOf, type HeadGraph } from '../../graphEdit';
  import { ORG_UNIT_TYPE, MEMBER_OF, newUserUnit } from '../../orgTypes';
  import { NS_ORGANISATION, POLICY_TYPE, USER_TYPE, newPolicyKey, policiesOf, policyProps, userKey, userProps, usersOf, type User } from '../../access';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { openTab } from '../../shell/tabs.svelte';

  let { tab }: { tab: Tab } = $props();

  let policies = $state<{ node: GraphNode; policy: Policy }[]>([]);
  let users = $state<{ node: GraphNode; user: User }[]>([]);
  let baselineId = '';
  let head: HeadGraph | undefined;
  let loading = $state(true);
  let error = $state('');
  let removing = $state(-1);

  async function load() {
    loading = true;
    error = '';
    try {
      const h = await headGraph(NS_ORGANISATION);
      baselineId = h.baselineId;
      head = h;
      policies = policiesOf(h.nodes);
      users = usersOf(h.nodes);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void stamp(keyOf.namespace(NS_ORGANISATION));
    load();
  });

  async function remove(n: GraphNode, p: Policy, i: number) {
    if (!(await confirmDialog({ message: `Delete the policy "${p.effect} ${p.resource}/${p.action}"?\n\n${p.rule}`, danger: true }))) return;
    removing = i;
    error = '';
    try {
      await applyOnMain(NS_ORGANISATION, `Delete policy ${p.resource}/${p.action}`, 'Delete an access policy', baselineId, [deleteNodeItem(n)]);
      await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      removing = -1;
    }
  }

  // --- users -----------------------------------------------------------------------

  /** the unit a user is a member of (link member_of), for the Organisation column */
  function orgOf(n: GraphNode): GraphNode | undefined {
    if (!head) return undefined;
    const l = currentLink(head, n, MEMBER_OF);
    return l?.to?.id ? head.nodes.find((u) => u.id === l.to?.id) : undefined;
  }

  function openUser(n: GraphNode) {
    openTab({ kind: 'user', params: { key: n.key ?? '' } });
  }

  let uSubject = $state('');
  let uName = $state('');
  let uEmail = $state('');
  let uAdding = $state(false);
  let uError = $state('');

  async function addUser(e: SubmitEvent) {
    e.preventDefault();
    const subject = uSubject.trim();
    if (!subject || uAdding) return;
    uAdding = true;
    uError = '';
    try {
      const u: User = { subject, displayName: uName.trim(), email: uEmail.trim(), locale: '' };
      // a user is a member of exactly one unit (ADR 0040): the one new users join (the waiting unit, ADR 0042)
      const units = (head?.nodes ?? []).filter((n) => n.type === ORG_UNIT_TYPE);
      const unit = units.find((n) => n.key === newUserUnit(units));
      if (!unit) throw new Error('no organisation unit to put the user in');
      await applyOnMain(NS_ORGANISATION, `User ${subject}`, 'Add a user', baselineId, [
        createNodeItem(userKey(subject), USER_TYPE, userProps(u), [{ type: MEMBER_OF, to: refOf(unit) }]),
      ]);
      uSubject = uName = uEmail = '';
      await load();
    } catch (err) {
      uError = errorMessage(err);
    } finally {
      uAdding = false;
    }
  }

  async function removeUser(n: GraphNode, u: User) {
    if (!(await confirmDialog({ message: `Delete the user "${u.subject}"?`, danger: true }))) return;
    error = '';
    try {
      await applyOnMain(NS_ORGANISATION, `Delete user ${u.subject}`, 'Delete a user', baselineId, [deleteNodeItem(n)]);
      await load();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  // --- add -----------------------------------------------------------------------

  let rule = $state('');
  let resource = $state('');
  let action = $state('');
  let policyEffect = $state<'allow' | 'deny'>('allow');
  let adding = $state(false);
  let addError = $state('');

  const canAdd = $derived(!!rule.trim() && !!resource.trim() && !!action.trim() && !adding);

  async function add(e: SubmitEvent) {
    e.preventDefault();
    if (!canAdd) return;
    adding = true;
    addError = '';
    try {
      const p: Policy = { rule: rule.trim(), resource: resource.trim(), action: action.trim(), effect: policyEffect };
      await applyOnMain(NS_ORGANISATION, `Policy ${p.resource}/${p.action}`, 'Add an access policy', baselineId, [
        createNodeItem(newPolicyKey(p), POLICY_TYPE, policyProps(p)),
      ]);
      rule = '';
      resource = '';
      action = '';
      policyEffect = 'allow';
      await load();
    } catch (err) {
      addError = errorMessage(err);
    } finally {
      adding = false;
    }
  }

  const EXAMPLES: { title: string; policy: Required<Policy> }[] = [
    {
      title: 'Four-eyes principle: a tech lead of the project, other than the author, applies the change',
      policy: {
        rule: 'hasRole(r.sub, "tech_lead") && r.sub.Subject != r.obj.Owner',
        resource: 'change',
        action: 'apply',
        effect: 'allow',
      },
    },
    {
      title: 'Anonymous users have no access to the methodology registry',
      policy: {
        rule: 'isAnonymous(r.sub)',
        resource: 'methodology',
        action: '*',
        effect: 'deny',
      },
    },
  ];

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: load }],
  );

  function useExample(p: Required<Policy>) {
    rule = p.rule;
    resource = p.resource;
    action = p.action;
    policyEffect = p.effect === 'deny' ? 'deny' : 'allow';
    document.getElementById('pol-rule')?.focus();
  }
</script>

<div class="editor-page">
<div class="editor-head">
  <Icon name="shield" size={18} />
  <h2>Access: policies and users</h2>
</div>

<p class="hint intro">
  Access control is attribute-based (ABAC). A request is authorized if at least one
  <em>allow</em> policy matches and no <em>deny</em> policy matches (deny takes precedence).
</p>

{#if error}<div class="alert">{error}</div>{/if}

<section class="card">
  <h3>Policies</h3>
  {#if loading && policies.length === 0}
    <p class="empty">Loading…</p>
  {:else if policies.length === 0 && !error}
    <p class="empty">No policies defined.</p>
  {:else if policies.length}
    <div class="scroll">
      <table>
        <thead>
          <tr>
            <th>Rule</th>
            <th>Resource</th>
            <th>Action</th>
            <th>Effect</th>
            <th><span class="sr-only">Delete</span></th>
          </tr>
        </thead>
        <tbody>
          {#each policies as { node, policy: p }, i (node.id)}
            <tr>
              <td class="rule"><code>{p.rule}</code></td>
              <td><code>{p.resource}</code></td>
              <td><code>{p.action}</code></td>
              <td><StatusBadge status={p.effect} /></td>
              <td class="actions">
                <button class="small danger" disabled={removing >= 0} onclick={() => remove(node, p, i)}>
                  {removing === i ? '…' : 'Delete'}
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<section class="card">
  <h3>Users</h3>
  <p class="hint">
    A user holds no role of their own: every role, administration included, is granted by an assignment, a platform one
    for <code>admin</code>, a project-scoped one for the others (ADR 0043, 0047); the latter is what <code>hasRole</code>
    sees for a resource of that project. The unit a user is a
    member of (link <code>member_of</code>, edited in the organisation) is their organisation when the token names none.
  </p>
  {#if users.length}
    <div class="scroll">
      <table>
        <thead>
          <tr><th>Subject</th><th>Name</th><th>Email</th><th>Organisation</th><th><span class="sr-only">Open</span></th></tr>
        </thead>
        <tbody>
          {#each users as { node, user } (node.id)}
            {@const unit = orgOf(node)}
            <tr>
              <td><code>{user.subject}</code></td>
              <td>{user.displayName}</td>
              <td>{user.email}</td>
              <td>{#if unit}<code>{String(unit.props?.['name'] ?? unit.key)}</code>{:else}<span class="muted">none</span>{/if}</td>
              <td class="actions">
                <button class="small" onclick={() => openUser(node)}>Open</button>
                <button class="small danger" onclick={() => removeUser(node, user)}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if !loading}
    <p class="empty">No users: callers have the roles of their token.</p>
  {/if}
  <form onsubmit={addUser}>
    <div class="pgrid">
      <div class="field"><label for="usr-sub">Subject</label><input id="usr-sub" type="text" class="mono" bind:value={uSubject} /></div>
      <div class="field"><label for="usr-name">Name</label><input id="usr-name" type="text" bind:value={uName} /></div>
      <div class="field"><label for="usr-mail">Email</label><input id="usr-mail" type="text" bind:value={uEmail} /></div>
    </div>
    {#if uError}<div class="alert">{uError}</div>{/if}
    <button class="primary" type="submit" disabled={!uSubject.trim() || uAdding}>{uAdding ? 'Adding…' : 'Add user'}</button>
  </form>
</section>

<div class="cols">
  <form class="card" onsubmit={add}>
    <h3>Add a policy</h3>
    <div class="field">
      <label for="pol-rule">Rule</label>
      <textarea
        id="pol-rule"
        class="mono"
        rows="3"
        spellcheck="false"
        bind:value={rule}
        placeholder={'hasRole(r.sub, "editor") && r.sub.Org == r.obj.Org'}
      ></textarea>
    </div>
    <div class="pgrid">
      <div class="field">
        <label for="pol-res">Resource</label>
        <input id="pol-res" type="text" class="mono" bind:value={resource} placeholder="change or *" />
      </div>
      <div class="field">
        <label for="pol-act">Action</label>
        <input id="pol-act" type="text" class="mono" bind:value={action} placeholder="apply or *" />
      </div>
      <div class="field">
        <label for="pol-eff">Effect</label>
        <select id="pol-eff" bind:value={policyEffect}>
          <option value="allow">allow</option>
          <option value="deny">deny</option>
        </select>
      </div>
    </div>
    {#if addError}<div class="alert">{addError}</div>{/if}
    <button class="primary" type="submit" disabled={!canAdd}>{adding ? 'Adding…' : 'Add'}</button>
  </form>

  <section class="card help">
    <h3>Writing a rule</h3>
    <p>
      The rule is a boolean expression evaluated for each request. The policy's resource and action
      (or <code>*</code>) filter which requests it applies to.
    </p>
    <dl>
      <dt><code>r.sub</code></dt>
      <dd>the requester: <code>Subject</code> (identifier), <code>Org</code>, <code>Roles</code></dd>
      <dt><code>r.obj</code></dt>
      <dd>
        the resource: <code>Type</code>, <code>ID</code>, <code>Org</code>, <code>Owner</code> (author),
        <code>Name</code>
      </dd>
      <dt><code>r.act</code></dt>
      <dd>the requested action (e.g. <code>apply</code>)</dd>
    </dl>
    <p>Functions: <code>hasRole(r.sub, "x")</code>, <code>hasAnyRole(r.sub, "a", "b")</code>, <code>isAnonymous(r.sub)</code>.</p>
    <h4>Examples</h4>
    {#each EXAMPLES as ex (ex.title)}
      <div class="example">
        <p>{ex.title}:</p>
        <pre>{ex.policy.rule}</pre>
        <div class="row">
          <span class="hint grow">
            <code>{ex.policy.resource}</code> / <code>{ex.policy.action}</code> →
            <StatusBadge status={ex.policy.effect} />
          </span>
          <button type="button" class="small" onclick={() => useExample(ex.policy)}>Use</button>
        </div>
      </div>
    {/each}
  </section>
</div>
</div>

<style>
  .intro {
    max-width: 60rem;
  }
  .scroll {
    overflow-x: auto;
  }
  .rule {
    min-width: 16rem;
  }
  .rule code {
    white-space: pre-wrap;
    word-break: break-word;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: 0 1rem;
    align-items: start;
  }
  .pgrid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
    gap: 0 0.85rem;
  }
  .help {
    font-size: 0.9rem;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.3rem 0.8rem;
    margin: 0 0 0.75rem;
  }
  dt {
    font-weight: 600;
  }
  dd {
    margin: 0;
  }
  .example {
    margin-bottom: 0.9rem;
  }
  .example p {
    margin-bottom: 0.3rem;
  }
  .example pre {
    margin-bottom: 0.35rem;
  }
</style>
