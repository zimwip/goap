<script lang="ts">
  // The people connected to the platform right now, with what each looks at (presence, ADR 0053).
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { onlineUsers } from '../flux/presence.svelte';
  import { shortId } from '../api';

  let open = $state(false);
  const users = $derived(onlineUsers());
  const others = $derived(users.filter((u) => !u.viewer.me).length);

  /** `node:<uuid>` → "node 1a2b3c4d" */
  const where = (id: string) => {
    const i = id.indexOf(':');
    const kind = id.slice(0, i);
    const key = id.slice(i + 1);
    return `${kind} ${key.length > 24 ? shortId(key) : key}`;
  };
</script>

<div class="item-wrap">
  <button
    type="button"
    class="online"
    aria-haspopup="dialog"
    aria-expanded={open}
    aria-label={`Online: ${users.length}`}
    title={others ? `${others} other${others === 1 ? '' : 's'} online` : 'Only you are online'}
    onclick={() => (open = !open)}
  >
    <Icon name="user" size={15} />
    <span class="n">{users.length}</span>
  </button>
  <Popover bind:open label="Online" align="right" placement="below" width="300px">
    <div class="pop-head"><strong>Online</strong></div>
    {#if users.length}
      <ul class="list">
        {#each users as u (u.viewer.subject)}
          <li>
            <span class="av" style:background={u.viewer.color}>{u.viewer.initials}</span>
            <span class="main">
              <strong>{u.viewer.subject}{u.viewer.me ? ' (you)' : ''}</strong>
              <span class="hint">{u.tabs.length ? u.tabs.map(where).join(', ') : 'idle'}</span>
            </span>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="pad hint">Nobody is connected.</p>
    {/if}
  </Popover>
</div>

<style>
  .online {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    padding: 0.35rem;
    min-height: 0;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    color: inherit;
  }
  .online:hover {
    background: var(--hover);
  }
  .n {
    font-size: 0.75rem;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0.25rem 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.3rem 0.6rem;
  }
  .av {
    display: inline-grid;
    place-items: center;
    flex: none;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    font-size: 0.65rem;
    font-weight: 600;
    color: #fff;
  }
  .main {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .main .hint {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
