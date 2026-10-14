// Introspection of the flow of a process (ProcessFlowView): what the context menu shows about a condition, a step, a
// boundary block or the level itself, built from what the flow already holds (the process graph, its levels, the
// dynamic conditions of the methodology with their CEL, the plan and the rules broken). Pure: the view renders it.
import type { Issue, LevelCheck, PlanPreview, ProcessGraph } from './api';

/** One line of a section: a label and a value, a CEL expression, or a link to a step or a condition. */
export interface InspectRow {
  label: string;
  value?: string;
  /** CEL (or another code) shown verbatim under the label */
  code?: string;
  /** the label is a step: open its inspection */
  step?: string;
  /** the label is a condition: open its inspection */
  condition?: string;
  tone?: 'warn' | 'bad' | 'ok';
}

export interface InspectSection {
  title: string;
  rows?: InspectRow[];
  /** a block of code (a CEL expression) */
  code?: string;
  text?: string;
}

export interface Inspection {
  kind: 'condition' | 'step' | 'boundary' | 'level';
  title: string;
  subtitle?: string;
  sections: InspectSection[];
}

export interface FlowFacts {
  graph: ProcessGraph;
  levels: LevelCheck[];
  /** the dynamic conditions of the methodology; undefined: unknown (every name is taken as declared) */
  conditions?: { name: string; expr?: string }[];
  plan?: PlanPreview;
  issues?: Issue[];
}

/** The CEL of a dynamic condition, undefined when the methodology does not declare it (or it has no expression). */
export function exprOf(f: FlowFacts, name: string): string | undefined {
  const e = f.conditions?.find((c) => c.name === name)?.expr;
  return e ? e : undefined;
}

/** A condition as it is wanted: its expression, negated when the step wants it false. */
export function wanted(expr: string, want: boolean): string {
  return want ? expr : `!(${expr})`;
}

/**
 * The conditions of an entry or an exit as one CEL expression (each wanted value, joined by &&), with the names whose
 * expression is unknown: they stay as names, so the expression reads but does not evaluate as is.
 */
export function combinedCel(f: FlowFacts, conds: Record<string, boolean> | undefined): { code: string; unknown: string[] } {
  const entries = Object.entries(conds ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const unknown: string[] = [];
  const parts = entries.map(([k, v]) => {
    const e = exprOf(f, k);
    if (!e) unknown.push(k);
    if (!e) return v ? k : `!${k}`;
    return v && entries.length > 1 ? `(${e})` : wanted(e, v);
  });
  return { code: parts.join('\n&& ') || 'true', unknown };
}

const yes = (v: boolean) => (v ? 'true' : 'false');

function conditionRows(f: FlowFacts, conds: Record<string, boolean> | undefined): InspectRow[] {
  return Object.entries(conds ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => {
      const e = exprOf(f, k);
      return { label: k, condition: k, value: `wants ${yes(v)}`, code: e, tone: e || !f.conditions ? undefined : ('warn' as const) };
    });
}

function combinedSection(f: FlowFacts, title: string, conds: Record<string, boolean> | undefined): InspectSection | undefined {
  if (!Object.keys(conds ?? {}).length) return undefined;
  const { code, unknown } = combinedCel(f, conds);
  return { title, code, text: unknown.length ? `Kept as names (no expression: a step's exit makes them true): ${unknown.join(', ')}.` : undefined };
}

/** The level a step is a direct step of. */
function levelOfStep(f: FlowFacts, path: string) {
  for (const l of f.levels) {
    const n = l.steps?.find((s) => s.path === path);
    if (n) return { level: l, node: n };
  }
  return undefined;
}

/** A condition: its CEL, where it comes from at the level shown, the steps and actions that set it and need it. */
export function inspectCondition(f: FlowFacts, name: string, level?: LevelCheck): Inspection {
  const e = exprOf(f, name);
  const sections: InspectSection[] = [];
  if (e) sections.push({ title: 'Expression (CEL)', code: e, text: 'Evaluated over the state of the change (its blackboard).' });
  else if (f.conditions)
    sections.push({ title: 'Expression (CEL)', text: 'No expression: this is not a dynamic condition of the methodology. Only the exit criteria of steps (the effects of their actions) make it true.' });
  else sections.push({ title: 'Expression (CEL)', text: 'The conditions of the methodology are not loaded yet.' });

  if (level) {
    const produced = (level.steps ?? []).filter((s) => s.exit && name in s.exit);
    const origin =
      level.inputs && name in level.inputs
        ? `given by the parent (wants ${yes(level.inputs[name])})`
        : produced.length
          ? `made by ${produced.length} step${produced.length === 1 ? '' : 's'} of this level`
          : e
            ? 'read from the state of the change'
            : 'unresolved: nothing at this level makes it true';
    const rows: InspectRow[] = [{ label: 'Comes from', value: origin, tone: origin.startsWith('unresolved') ? 'warn' : undefined }];
    if (level.outputs && name in level.outputs) rows.push({ label: 'Output of the level', value: `wants ${yes(level.outputs[name])}`, tone: 'ok' });
    for (const g of level.gaps ?? []) if (g.missing?.includes(name)) rows.push({ label: g.step ? `Gap at ${g.step}` : 'Gap of the level', value: g.message || g.kind, tone: g.kind === 'noop' ? 'bad' : 'warn' });
    sections.push({ title: `At level ${level.path ?? ''}`, rows });
  }

  const steps = f.graph.steps ?? [];
  const setBy = steps.filter((s) => s.exit && name in s.exit).map((s) => ({ label: s.path ?? '', step: s.path, value: `makes it ${yes(s.exit![name])}` }));
  const needBy = steps.filter((s) => s.entry && name in s.entry).map((s) => ({ label: s.path ?? '', step: s.path, value: `needs it ${yes(s.entry![name])}` }));
  sections.push({ title: 'Set by the steps', rows: setBy, text: setBy.length ? undefined : 'No step of this flow sets it.' });
  sections.push({ title: 'Needed by the steps', rows: needBy, text: needBy.length ? undefined : 'No step of this flow needs it.' });

  const acts: InspectRow[] = [];
  for (const a of f.graph.agents ?? []) {
    for (const x of a.actions ?? []) {
      if (x.effects && name in x.effects) acts.push({ label: `${a.name}/${x.name}`, value: `effect ${yes(x.effects[name])}${x.kind ? ` · ${x.kind}` : ''}` });
      if (x.pre && name in x.pre) acts.push({ label: `${a.name}/${x.name}`, value: `precondition ${yes(x.pre[name])}${x.kind ? ` · ${x.kind}` : ''}` });
    }
  }
  if (acts.length) sections.push({ title: 'Actions of the agents', rows: acts });
  return { kind: 'condition', title: name, subtitle: e ? 'dynamic condition' : 'condition', sections };
}

/** A step: how it runs, who answers for it, its conditions with their CEL, the plan, the gaps and the rules broken. */
export function inspectStep(f: FlowFacts, path: string, only?: 'conditions'): Inspection {
  const g = (f.graph.steps ?? []).find((s) => s.path === path);
  const at = levelOfStep(f, path);
  const n = at?.node;
  const entry = g?.entry ?? n?.entry;
  const exit = g?.exit ?? n?.exit;
  const name = g?.name ?? n?.name ?? path.split('/').pop() ?? path;
  const sections: InspectSection[] = [];

  if (!only) {
    const method = g?.method ?? n?.method;
    const target = g?.target ?? n?.target;
    const rows: InspectRow[] = [{ label: 'Path', value: path }];
    if (method) rows.push({ label: 'Runs as', value: target ? `${method} ${target}` : method });
    const cap = g?.capability ?? n?.capability;
    if (cap) rows.push({ label: 'Capability', value: cap });
    const foreach = g?.foreach ?? n?.foreach;
    if (foreach) rows.push({ label: 'For each', code: foreach });
    const groupBy = g?.groupBy ?? n?.groupBy;
    if (groupBy) rows.push({ label: 'Grouped by', code: groupBy });
    if (n?.composite) rows.push({ label: 'Sub-steps', value: String(n.subSteps ?? 0), tone: n.broken ? 'warn' : undefined });
    if (n?.broken) rows.push({ label: 'Incomplete', value: 'not resolved inside: the level cannot run through it', tone: 'warn' });
    sections.push({ title: 'Overview', rows, text: g?.description || undefined });
    if (cap) {
      const variants = (f.graph.methods ?? []).filter((m) => m.for === cap);
      sections.push({
        title: 'Methods providing the capability',
        rows: variants.map((m) => ({ label: m.name ?? '', value: m.priority ? `priority ${m.priority}` : m.when ? undefined : 'always applies', code: m.when || undefined })),
        text: variants.length ? 'The applicable one runs: highest priority, then the most specific when.' : 'No method provides it: the step cannot run.',
      });
    }
    const r = g?.roles;
    if (r && (r.responsible || r.accountable || r.consulted?.length || r.informed?.length)) {
      const rr: InspectRow[] = [];
      if (r.responsible) rr.push({ label: 'Responsible', value: r.responsible });
      if (r.accountable) rr.push({ label: 'Accountable', value: r.accountable });
      if (r.consulted?.length) rr.push({ label: 'Consulted', value: r.consulted.join(', ') });
      if (r.informed?.length) rr.push({ label: 'Informed', value: r.informed.join(', ') });
      sections.push({ title: 'Responsibilities (RACI)', rows: rr });
    }
  }

  sections.push({ title: 'Entry conditions', rows: conditionRows(f, entry), text: Object.keys(entry ?? {}).length ? undefined : 'None: it can start at once.' });
  const ce = combinedSection(f, 'Entry as one CEL expression', entry);
  if (ce) sections.push(ce);
  sections.push({ title: 'Exit criteria', rows: conditionRows(f, exit), text: Object.keys(exit ?? {}).length ? undefined : 'None.' });
  const cx = combinedSection(f, 'Exit as one CEL expression', exit);
  if (cx) sections.push(cx);

  if (!only) {
    const planned = (f.plan?.actions ?? []).filter((a) => a.step === path || a.step?.startsWith(`${path}/`));
    if (f.plan) {
      sections.push({
        title: 'In the plan',
        rows: planned.map((a) => ({ label: a.name ?? '', value: [a.kind, a.cost != null ? `cost ${a.cost}` : ''].filter(Boolean).join(' · ') })),
        text: planned.length ? undefined : 'The plan of the agent runs no action of this step.',
      });
    }
    const gaps: InspectRow[] = [];
    for (const gp of at?.level.gaps ?? []) {
      if (gp.step !== name) continue;
      gaps.push({ label: gp.kind ?? 'gap', value: gp.message || (gp.missing ?? []).join(', '), tone: gp.kind === 'noop' ? 'bad' : 'warn' });
    }
    const inner = f.levels.find((l) => l.path === path);
    for (const gp of inner?.gaps ?? []) gaps.push({ label: `inside: ${gp.step || 'outputs'}`, value: gp.message || (gp.missing ?? []).join(', '), tone: 'warn' });
    if (gaps.length) sections.push({ title: 'Gaps', rows: gaps });
    const ruled = (f.issues ?? []).filter((i) => i.activity === path).map((i) => ({ label: i.path ?? '', value: i.message ?? '', tone: 'bad' as const }));
    if (ruled.length) sections.push({ title: 'Rules broken', rows: ruled });
    if (g?.guidance) sections.push({ title: 'Guidance', text: g.guidance });
    if (g?.references?.length) sections.push({ title: 'References', rows: g.references.map((x) => ({ label: x.title || x.ref || '', value: [x.ref, x.section].filter(Boolean).join(' § ') })) });
  }
  return { kind: 'step', title: name, subtitle: only ? 'conditions (CEL)' : 'step', sections };
}

/** A boundary block (change state, inputs, outputs, unresolved, unreached): its conditions with their CEL. */
export function inspectBoundary(f: FlowFacts, title: string, conds: [string, boolean][], level?: LevelCheck): Inspection {
  const rows: InspectRow[] = conds.map(([k, v]) => {
    const e = exprOf(f, k);
    const needs = (level?.steps ?? []).filter((s) => s.entry && k in s.entry).map((s) => s.name ?? '');
    return { label: k, condition: k, value: [v ? '' : 'wants false', needs.length ? `needed by ${needs.join(', ')}` : ''].filter(Boolean).join(' · ') || undefined, code: e };
  });
  return { kind: 'boundary', title, subtitle: `${conds.length} condition${conds.length === 1 ? '' : 's'}`, sections: [{ title: 'Conditions', rows }] };
}

/** The level shown (the system of interest): who operates it, its inputs and outputs, the order of its steps, its gaps. */
export function inspectLevel(f: FlowFacts, level: LevelCheck): Inspection {
  const sections: InspectSection[] = [];
  const rows: InspectRow[] = [{ label: 'Path', value: level.path ?? '' }];
  if (level.kind) rows.push({ label: 'Kind', value: level.kind });
  if (level.agent) rows.push({ label: 'Agent', value: level.agent });
  if (level.goal) rows.push({ label: 'Goal', value: level.goal });
  rows.push({ label: 'Consistent', value: level.ok ? 'yes' : 'no', tone: level.ok ? 'ok' : 'warn' });
  sections.push({ title: 'Overview', rows });
  sections.push({ title: 'Inputs (given by the parent)', rows: conditionRows(f, level.inputs), text: Object.keys(level.inputs ?? {}).length ? undefined : 'None.' });
  sections.push({ title: 'Outputs (to make true)', rows: conditionRows(f, level.outputs), text: Object.keys(level.outputs ?? {}).length ? undefined : 'None.' });
  const layers = new Map<number, string[]>();
  for (const o of level.order ?? []) layers.set(o.layer ?? 0, [...(layers.get(o.layer ?? 0) ?? []), o.name ?? '']);
  if (layers.size)
    sections.push({
      title: 'Order the conditions allow',
      rows: [...layers.entries()].sort(([a], [b]) => a - b).map(([k, ns]) => ({ label: `${k + 1}`, value: ns.join(', ') })),
      text: 'Steps on one line are independent.',
    });
  if (level.gaps?.length) sections.push({ title: 'Gaps', rows: level.gaps.map((g) => ({ label: g.step || 'outputs', value: g.message || `${g.kind}: ${(g.missing ?? []).join(', ')}`, tone: g.kind === 'noop' ? 'bad' : 'warn' })) });
  return { kind: 'level', title: (level.path ?? '').split('/').pop() || 'Level', subtitle: 'level', sections };
}

/** The inspection as plain text, for the clipboard. */
export function inspectionText(i: Inspection): string {
  const out = [`${i.title}${i.subtitle ? ` (${i.subtitle})` : ''}`];
  for (const s of i.sections) {
    out.push('', `## ${s.title}`);
    if (s.text) out.push(s.text);
    for (const r of s.rows ?? []) {
      out.push(`- ${r.label}${r.value ? `: ${r.value}` : ''}`);
      if (r.code) out.push(`    ${r.code.replace(/\n/g, '\n    ')}`);
    }
    if (s.code) out.push(s.code);
  }
  return out.join('\n');
}
