<script lang="ts">
  // The action flow of a change as a graph: the steps of a run left to right, and
  // every relaunch forking into a lane of its own (dashed while open, solid once
  // adopted, faded when discarded). Steps of a run replaced by an adopted flow are faded.
  import { formatDuration, int, shortId, type Flow, type Process, type Step } from '../api';
  import { chainOf, offsetOf, relaunchesOf, rootOf, runKind, type ProcessLookup } from '../flowChain';
  import FlowActions from './FlowActions.svelte';

  let {
    processes,
    changeId = '',
    processId = '',
    flows = [],
    proposal,
    onopen,
    ondecided,
  }: {
    /** the processes store */
    processes: ProcessLookup;
    /** draw every chain of this change (used when no process is given) */
    changeId?: string;
    /** draw the chain of this process */
    processId?: string;
    flows?: Flow[];
    /** a proposed restart (blackboard check): the step is highlighted */
    proposal?: { process?: string; step?: number };
    onopen?: (processId: string) => void;
    /** an open flow was adopted or discarded: reload the flows */
    ondecided?: () => void;
  } = $props();

  const W = 128;
  const H = 34;
  const GX = 30;
  const GY = 52;
  const PAD = 12;
  const LABEL = 22;

  interface Node {
    key: string;
    x: number;
    y: number;
    step: Step;
    proc: Process;
    abs: number;
    faded: boolean;
    state: 'ok' | 'partial' | 'error' | 'pending';
  }
  interface Link {
    d: string;
    kind: 'lane' | 'open' | 'adopted' | 'discarded';
  }
  interface Tag {
    x: number;
    y: number;
    text: string;
    cls: string;
    strike?: boolean;
  }

  function stateOf(s: Step): Node['state'] {
    if (s.error) return 'error';
    if (!s.endedAt) return 'pending';
    return s.effectsMet ? 'ok' : 'partial';
  }

  const roots = $derived.by(() => {
    if (processId) {
      const p = processes.get(processId);
      return p ? [rootOf(p, processes)] : [];
    }
    const seen = new Set<string>();
    const out: Process[] = [];
    for (const p of processes.values()) {
      if (p.changeId !== changeId || p.parentId) continue;
      const r = rootOf(p, processes);
      if (r.id && !seen.has(r.id) && relaunchesOf(r, processes).length > 0) {
        seen.add(r.id);
        out.push(r);
      }
    }
    return out;
  });

  const flowById = $derived(new Map(flows.map((f) => [f.id ?? '', f])));
  const graphChange = $derived(changeId || processes.get(processId)?.changeId || '');
  // flows still open: their adopt / discard actions sit under the graph, whatever state their run is in
  const openFlows = $derived(flows.filter((f) => f.status === 'open'));

  const layout = $derived.by(() => {
    const nodes: Node[] = [];
    const links: Link[] = [];
    const tags: Tag[] = [];
    let lane = 0;
    let cols = 1;
    for (const root of roots) {
      const chain = chainOf(root, processes);
      const laneOf = new Map<string, number>();
      chain.forEach((p) => laneOf.set(p.id ?? '', lane++));
      const at = (p: Process, abs: number) => ({
        x: PAD + abs * (W + GX),
        y: PAD + LABEL + (laneOf.get(p.id ?? '') ?? 0) * (H + GY),
      });
      // steps of the runs not replaced by an adopted flow stay solid
      const adoptedFrom = (p: Process) => {
        const fromAbs = relaunchesOf(p, processes)
          .filter((c) => runKind(c) === 'adopted')
          .map((c) => offsetOf(c, processes));
        return fromAbs.length ? Math.min(...fromAbs) : Infinity;
      };
      for (const p of chain) {
        const off = offsetOf(p, processes);
        const cut = adoptedFrom(p);
        const steps = p.steps ?? [];
        const kind = runKind(p);
        for (let i = 0; i < steps.length; i++) {
          const abs = off + (steps[i].index ?? i);
          const pos = at(p, abs);
          nodes.push({ key: `${p.id}:${i}`, ...pos, step: steps[i], proc: p, abs, state: stateOf(steps[i]), faded: abs >= cut || kind === 'discarded' || kind === 'replaced' });
          cols = Math.max(cols, abs + 1);
        }
        const last = steps.length ? off + (steps[steps.length - 1].index ?? steps.length - 1) : off - 1;
        const first = at(p, off);
        // end marker
        const endAt = at(p, Math.max(last + 1, off));
        if (p.status === 'waiting' && p.pending?.kind === 'flow') tags.push({ x: endAt.x, y: endAt.y + H / 2, text: '◆ awaiting decision', cls: 'open' });
        else if (p.flow && kind === 'adopted') tags.push({ x: endAt.x, y: endAt.y + H / 2, text: '✓ adopted', cls: 'adopted' });
        else if (p.flow && kind === 'discarded') tags.push({ x: endAt.x, y: endAt.y + H / 2, text: '✕ discarded', cls: 'discarded' });
        else if (kind === 'replaced') tags.push({ x: endAt.x, y: endAt.y + H / 2, text: 'replaced', cls: 'discarded' });
        const rivals = p.flow ? flowById.get(p.flow)?.competesWith : undefined;
        if (rivals?.length && flowById.get(p.flow ?? '')?.status === 'open') {
          tags.push({ x: endAt.x, y: endAt.y + H / 2 + 14, text: `⚠ competes with ${shortId(rivals[0])}`, cls: 'competing' });
        }
        cols = Math.max(cols, Math.max(last + 1, off) + 2);
        if (p.flow) {
          // fork from the parent lane: the step before the restarted one (or the start of the run)
          const parent = p.relaunchOf ? processes.get(p.relaunchOf) : undefined;
          if (parent) {
            const from = at(parent, Math.max(off - 1, 0));
            const sx = off > 0 ? from.x + W : from.x;
            const sy = from.y + H / 2;
            const ex = first.x;
            const ey = first.y + H / 2;
            const mx = (sx + ex) / 2;
            links.push({ d: `M ${sx} ${sy} C ${mx} ${sy}, ${mx} ${ey}, ${ex} ${ey}`, kind: kind === 'open' ? 'open' : kind === 'adopted' ? 'adopted' : 'discarded' });
            const f = flowById.get(p.flow);
            const stale = f?.stale?.length ?? 0;
            if (stale) tags.push({ x: sx + 6, y: sy - 8, text: `${stale} stale`, cls: 'stale' });
          }
        }
        // lane connectors
        const own = nodes.filter((n) => n.proc === p).sort((a, b) => a.abs - b.abs);
        for (let i = 1; i < own.length; i++) {
          const a = own[i - 1];
          const b = own[i];
          links.push({ d: `M ${a.x + W} ${a.y + H / 2} L ${b.x} ${b.y + H / 2}`, kind: 'lane' });
        }
      }
    }
    const width = PAD * 2 + cols * (W + GX);
    const height = PAD * 2 + LABEL + Math.max(lane, 1) * (H + GY) - GY + 8;
    return { nodes, links, tags, width, height };
  });

  const laneLabels = $derived.by(() => {
    const out: { x: number; y: number; text: string; cls: string; id: string }[] = [];
    let lane = 0;
    for (const root of roots) {
      for (const p of chainOf(root, processes)) {
        const y = PAD + LABEL + lane * (H + GY) - 8;
        const f = p.flow ? flowById.get(p.flow) : undefined;
        const kind = runKind(p);
        const text = p.flow
          ? `flow ${shortId(p.flow)} · from step ${(p.fromStep ?? 0) + 1}${f?.reason ? ` · ${f.reason}` : ''}${f?.competesWith?.length ? ' · competing' : ''}`
          : p.title || shortId(p.id);
        out.push({ x: PAD, y, text, cls: kind, id: p.id ?? '' });
        lane++;
      }
    }
    return out;
  });

  function tip(n: Node): string {
    const s = n.step;
    const ms = s.startedAt && s.endedAt ? new Date(s.endedAt).getTime() - new Date(s.startedAt).getTime() : NaN;
    const parts = [`#${n.abs + 1} ${s.action}`, s.error ? 'error' : !s.endedAt ? 'in progress' : s.effectsMet ? 'effects reached' : 'effects not reached'];
    if (Number.isFinite(ms) && ms >= 0) parts.push(formatDuration(ms));
    if (s.items?.length) parts.push(`${s.items.length} item(s)`);
    if (int(s.usage?.llmCalls)) parts.push(`${s.usage?.llmCalls} LLM`);
    return parts.join(' · ');
  }

  function short(s: string | undefined, n = 16): string {
    const t = s ?? '';
    return t.length > n ? `${t.slice(0, n - 1)}…` : t;
  }
</script>

{#if layout.nodes.length}
  <div class="scroll">
    <svg width={layout.width} height={layout.height} viewBox="0 0 {layout.width} {layout.height}" role="img" aria-label="Action flow and its branches">
      {#each laneLabels as l (l.id)}
        <text class="lane {l.cls}" x={l.x} y={l.y}>{short(l.text, 60)}</text>
      {/each}
      {#each layout.links as k, i (i)}
        <path class="link {k.kind}" d={k.d} fill="none" />
      {/each}
      {#each layout.nodes as n (n.key)}
        <g
          class="node {n.state}"
          class:faded={n.faded}
          class:current={n.proc.id === processId}
          class:proposed={!!proposal && n.proc.id === proposal.process && n.step.index === proposal.step}
          role="button"
          tabindex="0"
          onclick={() => n.proc.id && onopen?.(n.proc.id)}
          onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && n.proc.id && onopen?.(n.proc.id)}
        >
          <title>{tip(n)}</title>
          {#if proposal && n.proc.id === proposal.process && n.step.index === proposal.step}
            <rect class="ring" x={n.x - 4} y={n.y - 4} width={W + 8} height={H + 8} rx="9" />
            <text class="tag warn" x={n.x} y={n.y - 9}>proposed restart</text>
          {/if}
          <rect x={n.x} y={n.y} width={W} height={H} rx="6" />
          <text x={n.x + 8} y={n.y + 14} class="idx">#{n.abs + 1}</text>
          <text x={n.x + 8} y={n.y + 27} class="act">{short(n.step.action)}</text>
        </g>
      {/each}
      {#each layout.tags as t, i (i)}
        <text class="tag {t.cls}" x={t.x} y={t.y} dominant-baseline="middle">{t.text}</text>
      {/each}
    </svg>
  </div>
  {#if openFlows.length && graphChange}
    <ul class="open-flows">
      {#each openFlows as f (f.id)}
        <li>
          <span class="tag-open">open</span>
          <code>{shortId(f.id)}</code>
          <span class="hint">from step {(f.fromStep ?? 0) + 1}{f.reason ? ` · ${f.reason}` : ''}</span>
          {#if f.competesWith?.length}<span class="tag-competing">competes with {f.competesWith.map((c) => shortId(c)).join(', ')}</span>{/if}
          <FlowActions flow={f} changeId={graphChange} compact {ondecided} />
        </li>
      {/each}
    </ul>
  {/if}
{:else}
  <p class="empty">No relaunched flow yet.</p>
{/if}

<style>
  .scroll {
    overflow-x: auto;
    max-width: 100%;
  }
  svg {
    display: block;
    font-family: var(--font);
  }
  .node .ring {
    fill: none;
    stroke: var(--warn);
    stroke-width: 2.5;
  }
  .node {
    cursor: pointer;
  }
  .node rect {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1.5;
  }
  .node.ok rect {
    fill: var(--ok-soft);
    stroke: var(--ok);
  }
  .node.partial rect {
    fill: var(--warn-soft);
    stroke: var(--warn);
  }
  .node.error rect {
    fill: var(--danger-soft);
    stroke: var(--danger);
  }
  .node.pending rect {
    fill: var(--accent-soft);
    stroke: var(--accent);
  }
  .node.current rect {
    stroke-width: 3;
  }
  .node.faded {
    opacity: 0.4;
  }
  .node:hover rect,
  .node:focus-visible rect {
    stroke-width: 3;
    outline: none;
  }
  .idx {
    font-size: 10px;
    fill: var(--muted);
  }
  .act {
    font-size: 12px;
    font-weight: 600;
    fill: var(--text);
  }
  .link {
    stroke: var(--border);
    stroke-width: 2;
  }
  .link.open {
    stroke: var(--accent);
    stroke-dasharray: 6 4;
  }
  .link.adopted {
    stroke: var(--ok);
  }
  .link.discarded {
    stroke: var(--muted);
    stroke-dasharray: 2 4;
    opacity: 0.6;
  }
  .lane {
    font-size: 11px;
    fill: var(--muted);
  }
  .lane.open {
    fill: var(--accent);
  }
  .lane.adopted {
    fill: var(--ok);
  }
  .lane.discarded,
  .lane.replaced {
    text-decoration: line-through;
    opacity: 0.7;
  }
  .tag {
    font-size: 11px;
    fill: var(--muted);
  }
  .tag.open {
    fill: var(--accent);
    font-weight: 600;
  }
  .tag.adopted {
    fill: var(--ok);
    font-weight: 600;
  }
  .tag.discarded {
    fill: var(--muted);
  }
  .tag.stale,
  .tag.competing {
    fill: var(--warn);
    font-weight: 600;
  }
  .open-flows {
    list-style: none;
    margin: 6px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .open-flows li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .tag-open {
    color: var(--accent);
    font-weight: 600;
  }
  .tag-competing {
    color: var(--warn);
    font-weight: 600;
  }
  .empty {
    color: var(--muted);
  }
</style>
