<script lang="ts">
  let { status = '' }: { status?: string } = $props();

  const LABELS: Record<string, string> = {
    clarifying: 'clarifying',
    running: 'running',
    waiting: 'waiting',
    completed: 'completed',
    stuck: 'stuck',
    failed: 'failed',
    draft: 'draft',
    active: 'active',
    merge_pending: 'merge pending',
    applied: 'applied',
    abandoned: 'abandoned',
    proposed: 'proposed',
    accepted: 'accepted',
    rejected: 'rejected',
    superseded: 'superseded',
    stale: 'stale',
    candidate: 'candidate',
    open: 'open',
    adopted: 'adopted',
    discarded: 'discarded',
    competing: 'competing',
    published: 'published',
    archived: 'archived',
    allow: 'allow',
    deny: 'deny',
  };

  const TONES: Record<string, string> = {
    clarifying: 'info',
    running: 'accent',
    waiting: 'warn',
    completed: 'ok',
    stuck: 'warn',
    failed: 'danger',
    active: 'accent',
    merge_pending: 'warn',
    applied: 'ok',
    abandoned: 'neutral',
    proposed: 'info',
    accepted: 'ok',
    rejected: 'danger',
    superseded: 'neutral',
    stale: 'warn',
    candidate: 'info',
    open: 'info',
    adopted: 'ok',
    discarded: 'neutral',
    competing: 'warn',
    published: 'ok',
    archived: 'neutral',
    allow: 'ok',
    deny: 'danger',
  };

  const tone = $derived(TONES[status] ?? 'neutral');
</script>

<span class="badge {tone}" title={status}>
  {#if status === 'running'}<span class="pulse" aria-hidden="true"></span>{/if}
  {LABELS[status] ?? (status || '—')}
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.05rem 0.55rem;
    border-radius: 999px;
    font-size: 0.78rem;
    font-weight: 600;
    white-space: nowrap;
    background: var(--neutral-soft);
    color: var(--muted);
  }
  .accent {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .info {
    background: var(--info-soft);
    color: var(--info);
  }
  .ok {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .warn {
    background: var(--warn-soft);
    color: var(--warn);
  }
  .danger {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .pulse {
    width: 0.45rem;
    height: 0.45rem;
    border-radius: 50%;
    background: currentColor;
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.25;
    }
  }
</style>
