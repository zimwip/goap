<script lang="ts">
  // Header indicator of the person's LLM consumption today (local day), read from the gateway's ledger (ADR 0089):
  // one `UsageSummary` since midnight of the calls attributed to them, their own and those made on their behalf (ADR
  // 0114), grouped by who acted, refreshed every minute while the page is visible and when the usage feed sees new
  // calls. A click opens the Token usage dashboard on "Today".
  import Icon from './Icon.svelte';
  import { openUsage, usageState } from './usageState.svelte';
  import { models, errorMessage } from '../api';
  import { me } from '../stores/session.svelte';
  import { usage } from '../stores/usage.svelte';
  import { compactTokens, directSplit, periodStart, type Totals } from '../tokenStats';

  const REFRESH_MS = 60_000;

  let totals = $state<Totals | null>(null);
  let indirect = $state<Totals | null>(null);
  let error = $state('');
  let epoch = 0;

  async function load(): Promise<void> {
    if (typeof document !== 'undefined' && document.hidden) return;
    const mine = ++epoch;
    const from = periodStart('today', Date.now());
    try {
      const res = await models.usageSummary({ endUser: me(), from: new Date(from).toISOString() }, 'subject');
      if (mine !== epoch) return;
      const split = directSplit(res.rows, me());
      totals = split.all;
      indirect = split.indirect;
      error = '';
    } catch (e) {
      if (mine === epoch) error = errorMessage(e);
    }
  }

  $effect(() => {
    void load();
    const timer = setInterval(() => void load(), REFRESH_MS);
    const onVisible = () => void load();
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  });

  // new calls seen by the feed (while the dashboard is open), or the dashboard closed: the figure follows
  $effect(() => {
    void usage.version;
    if (usageState.open) return;
    const t = setTimeout(() => void load(), 400);
    return () => clearTimeout(t);
  });

  const n = (v: number) => Math.round(v).toLocaleString('en-US');
  const title = $derived(
    error
      ? `Today's LLM usage is unavailable: ${error}`
      : totals
        ? `Your LLM usage today: ${n(totals.total)} tokens (${n(totals.input)} in, ${n(totals.output)} out) over ${n(totals.calls)} call${totals.calls === 1 ? '' : 's'}${totals.errors ? `, ${n(totals.errors)} failed` : ''}${indirect?.calls ? `, of which ${n(indirect.total)} tokens made on your behalf by what your actions set off` : ''}. Click for the details.`
        : "Loading today's LLM usage",
  );
</script>

<button type="button" class="usage" class:err={!!error} {title} aria-label={title} aria-haspopup="dialog" onclick={() => openUsage('today')}>
  <Icon name="coins" size={14} />
  <span class="v">{error ? '—' : totals ? compactTokens(totals.total) : '…'}</span>
  <span class="l">today</span>
</button>

<style>
  .usage {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0 0.35rem;
    min-height: 0;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    font-size: 0.85rem;
    color: var(--muted);
    white-space: nowrap;
  }
  .usage:hover {
    background: var(--hover);
    color: var(--text);
  }
  .v {
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--text);
  }
  .usage.err .v {
    color: var(--muted);
  }
  @media (max-width: 900px) {
    .l {
      display: none;
    }
  }
</style>
