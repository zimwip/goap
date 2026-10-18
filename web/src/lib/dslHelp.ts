// What the "DSL Help" panel shows for the editor in front of the person (ADR 0018): the editors say what
// script they hold (`EditorView.help`, a DslTarget), this module turns it into the page: the `ctx` API of
// that usage, its shapes, a starter script, and the sections of docs/dsl.md worth opening.
import type { TabSpec } from './shell/types';
import {
  ALGORITHM_TEMPLATES,
  ALGORITHM_USAGES,
  DSL_FUNCTIONS,
  DSL_SHAPES,
  ACTION_TEMPLATE,
  type AlgorithmUsage,
  type DslFunction,
  type DslGroup,
  type DslShape,
} from './dsl';

export type HelpUsage = 'action' | AlgorithmUsage;

/** A declared parameter of an algorithm, with the value of an instance when the editor holds one. */
export interface HelpParam {
  name: string;
  type: string;
  description: string;
  required: boolean;
  /** text form of the default, "" when none */
  defaultValue: string;
  /** enum values, comma-separated */
  values: string;
  /** current value (an instance), undefined when the editor has none */
  value?: unknown;
}

/** A place the DSL is written in the draft in front of the person (a script action, an algorithm). */
export interface HelpLink {
  label: string;
  detail?: string;
  spec: TabSpec;
}

/** What an editor tells the panel about the script it holds. */
export interface DslTarget {
  /** no usage: the editor holds no script (the panel says why in `note`) */
  usage?: HelpUsage;
  /** name of the object being edited */
  title: string;
  /** its kind: "Script action · sdlc v3" */
  subtitle: string;
  note?: string;
  params?: HelpParam[];
  /** where an algorithm instance is plugged */
  plugs?: string[];
  /** other scripts of the same draft, to jump to */
  links?: { heading: string; items: HelpLink[] };
}

export interface UsageHelp {
  usage: HelpUsage;
  title: string;
  description: string;
  /** what the script returns, how it rejects */
  contract: string;
  /** where the code is written / plugged in the IDE */
  where: string;
  functions: DslFunction[];
  shapes: DslShape[];
  /** the `## ` sections of docs/dsl.md that describe it */
  reference: string[];
  /** input of "Try it" (algorithms) */
  sample?: Record<string, unknown>;
}

const shapes = (...names: string[]): DslShape[] => names.map((n) => DSL_SHAPES[n]).filter(Boolean);

const WHERE: Record<HelpUsage, string> = {
  action: 'Methodology › Actions › an action of kind script',
  property_validator: 'Domain › Node type › attribute › validators (an instance of the algorithm)',
  node_validator: 'Domain › Node type › validators (an instance of the algorithm)',
  link_validator: 'Domain › Link type › validators (an instance of the algorithm)',
  transition_guard: 'Domain › Lifecycle › transition › guards (an instance of the algorithm)',
  transition_action: 'Domain › Lifecycle › transition › actions (an instance of the algorithm)',
  adapter: 'Platform › Adapters: instantiated by an organisational unit with its parameter values',
};

const ALGORITHM_SHAPES: Record<AlgorithmUsage, string[]> = {
  property_validator: ['Node'],
  node_validator: ['Node'],
  link_validator: ['Node'],
  transition_guard: ['Node', 'Change', 'Transition'],
  transition_action: ['Node', 'Change', 'Transition'],
  adapter: [],
};

const ACTION_USAGE: UsageHelp = {
  usage: 'action',
  title: 'Script action',
  description:
    'Code of an action of kind script, run by the engine in the process sandbox on the blackboard of the change. Writes are buffered and reach the change atomically when the action ends: an action that fails writes nothing. The #nN references returned by the writes designate change impacts declared in the same execution.',
  contract: 'Reads the change, writes to it (impacts, drafts, reviews, decisions) and may call a model, a sub-agent or an MCP tool. Fails by throwing.',
  where: WHERE.action,
  functions: DSL_FUNCTIONS,
  shapes: shapes('Item', 'Node', 'Link', 'ChangeImpact', 'Option', 'DecisionPoint'),
  reference: ['Script actions', 'Reading', 'Writing', 'Calls', 'Examples'],
};

/** The help of a usage: the action's ctx, or an algorithm usage's. */
export function usageHelp(usage: HelpUsage): UsageHelp {
  if (usage === 'action') return ACTION_USAGE;
  const u = ALGORITHM_USAGES.find((x) => x.usage === usage);
  if (!u) return ACTION_USAGE;
  return {
    usage,
    title: u.title,
    description: u.description,
    contract: u.contract,
    where: WHERE[usage],
    functions: u.functions,
    shapes: shapes(...ALGORITHM_SHAPES[usage]),
    reference: ['Algorithms'],
    sample: u.sample,
  };
}

/** Every usage, to tell where the DSL is written when no editor in front of the person holds a script. */
export function overview(): { usage: HelpUsage; title: string; description: string; where: string }[] {
  return (['action', ...ALGORITHM_USAGES.map((u) => u.usage)] as HelpUsage[]).map((usage) => {
    const h = usageHelp(usage);
    return { usage, title: h.title, description: h.description, where: h.where };
  });
}

/** Starter script of a usage (what a new script starts from). */
export function starter(usage: HelpUsage): string {
  if (usage === 'action') return ACTION_TEMPLATE;
  return ALGORITHM_TEMPLATES[usage] ?? '';
}

/** `ctx.name(a, b)`. */
export function signature(fn: DslFunction): string {
  return `ctx.${fn.name}(${fn.args.join(', ')})`;
}

/** The names of a shape's fields. */
export function fieldNames(shape: DslShape): string[] {
  return shape.fields;
}

export const GROUP_TITLES: Record<DslGroup, string> = {
  read: 'Read',
  write: 'Write',
  call: 'Call',
};

export function matches(fn: DslFunction, query: string): boolean {
  const q = query.trim().toLowerCase();
  return !q || fn.name.toLowerCase().includes(q) || fn.doc.toLowerCase().includes(q) || fn.returns.toLowerCase().includes(q);
}

/** The functions that match a search, in the three groups (an empty group is left out). */
export function groupFunctions(fns: DslFunction[], query = ''): { group: DslGroup; title: string; fns: DslFunction[] }[] {
  return (['read', 'write', 'call'] as DslGroup[])
    .map((group) => ({ group, title: GROUP_TITLES[group], fns: fns.filter((f) => f.group === group && matches(f, query)) }))
    .filter((g) => g.fns.length > 0);
}

/** Default and enum of a declared parameter, as the person reads them. */
export function paramNote(p: HelpParam): string {
  const out: string[] = [];
  if (p.required) out.push('required');
  if (p.values) out.push(`one of ${p.values}`);
  if (p.defaultValue) out.push(`default ${p.defaultValue}`);
  return out.join(' · ');
}

/** How a script reads a declared parameter. */
export function paramAccess(p: HelpParam): string {
  return `ctx.param("${p.name}")`;
}

export interface Section {
  title: string;
  body: string;
}

/** Splits a Markdown document on its `## ` headings (a fenced block is never split); the text before the first one is `title: ""`. */
export function splitSections(md: string): Section[] {
  const out: Section[] = [];
  let cur: Section = { title: '', body: '' };
  let fenced = false;
  for (const line of md.replace(/\r\n/g, '\n').split('\n')) {
    if (line.startsWith('```')) fenced = !fenced;
    const h = !fenced && /^##\s+(.*)$/.exec(line);
    if (h) {
      out.push(cur);
      cur = { title: h[1].trim(), body: '' };
    } else cur.body += line + '\n';
  }
  out.push(cur);
  return out.filter((s) => s.title || s.body.trim());
}

/** Is a section one the usage's help points to (the reference names are prefixes of the titles)? */
export function isRelevant(section: Section, usage: HelpUsage | undefined): boolean {
  return !!usage && usageHelp(usage).reference.some((r) => section.title.startsWith(r));
}
