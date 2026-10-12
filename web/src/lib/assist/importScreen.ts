// What the Import tab tells the assistant (ADR 0092, ADR 0104): what it imports and a description of the content loaded
// (format, name or namespace, number of nodes), never the content itself.

export type ImportWhat = 'methodology' | 'domain' | 'graph';

/** One line on the content loaded in the Import tab: its size and what it names, read without sending it. */
export function contentFacts(what: ImportWhat, text: string): string {
  const t = text.trim();
  if (!t) return 'no content loaded yet (the person picks a file or pastes it)';
  const size = `${new TextEncoder().encode(t).length} bytes`;
  if (what === 'graph') {
    try {
      const s = JSON.parse(t) as { format?: unknown; namespace?: unknown; nodes?: unknown };
      if (s?.format !== 'goap.graph.snapshot/v1') return `content loaded (${size}), not a graph snapshot (format goap.graph.snapshot/v1 expected)`;
      const nodes = Array.isArray(s.nodes) ? s.nodes.length : 0;
      return `a graph snapshot of namespace ${String(s.namespace ?? '?')}, ${nodes} node${nodes === 1 ? '' : 's'} (${size})`;
    } catch {
      return `content loaded (${size}), not valid JSON`;
    }
  }
  let name = '';
  if (t.startsWith('{')) {
    try {
      name = String((JSON.parse(t) as { name?: unknown }).name ?? '');
    } catch {
      return `content loaded (${size}), not valid JSON`;
    }
  } else name = /^name:\s*["']?([^"'\s#]+)/m.exec(t)?.[1] ?? '';
  return `a ${what} definition${name ? ` named ${name}` : ' (no name found)'} (${size})`;
}

export interface ImportOutcome {
  /** a definition imported */
  definition?: { name: string; version: string; status?: string; issues: number };
  /** a graph snapshot imported */
  graph?: { changeTitle?: string; created?: number; updated?: number; unchanged?: number; warnings?: number };
}

/** One sentence on the Import tab: what it imports, the content loaded, the options and the last outcome. */
export function importSummary(what: ImportWhat, text: string, opts: { publish?: boolean; title?: string }, outcome: ImportOutcome = {}): string {
  const parts = [
    `Import of a ${what === 'graph' ? 'graph snapshot (opens a change on main, reviewed, committed and merged as usual)' : `${what} (YAML or JSON)`}`,
    contentFacts(what, text),
    what === 'graph' ? (opts.title ? `change title “${opts.title}”` : '') : opts.publish ? 'published directly' : 'imported as a draft',
  ];
  const d = outcome.definition;
  if (d) parts.push(`last import: ${d.name} v${d.version} ${d.status === 'published' ? 'published' : 'draft'}, ${d.issues} issue${d.issues === 1 ? '' : 's'}`);
  const g = outcome.graph;
  if (g)
    parts.push(
      g.changeTitle
        ? `last import: change “${g.changeTitle}”, ${g.created ?? 0} created, ${g.updated ?? 0} modified, ${g.unchanged ?? 0} unchanged, ${g.warnings ?? 0} warning${g.warnings === 1 ? '' : 's'}`
        : `last import: nothing differed (${g.unchanged ?? 0} unchanged), no change created`,
    );
  return parts.filter(Boolean).join('; ') + '.';
}
