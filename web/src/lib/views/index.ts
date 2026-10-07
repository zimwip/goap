// Registration of the workbench's views in the shell's registry.
import { registerView } from '../shell/registry';
import type { Tab } from '../shell/types';
import { formatDate, formatInt, shortId } from '../api';
import { peekDraft, drafts } from '../stores/drafts.svelte';
import { peekDomainDraft, domainDrafts } from '../stores/domains.svelte';
import { processes, live } from '../stores/live.svelte';
import { changes } from '../stores/catalog.svelte';
import NodeTab from './editors/NodeTab.svelte';
import { draftGroup, findStep, KIND_SECTION, SECTION_ICON } from './editors/methodologyTabs';
import { activeDraft } from './bottom/activeDraft';
import { domainGroup } from './editors/domainTabs';
import './nodeEditors';

import MethodologyExplorer from './nav/MethodologyExplorer.svelte';
import DomainExplorer from './nav/DomainExplorer.svelte';
import BaselineExplorer from './nav/BaselineExplorer.svelte';
import BaselineWorkspace from './nav/BaselineWorkspace.svelte';
import ChangesExplorer from './nav/ChangesExplorer.svelte';
import AccessExplorer from './nav/AccessExplorer.svelte';
import PeopleOrgExplorer from './nav/PeopleOrgExplorer.svelte';
import AdaptersExplorer from './nav/AdaptersExplorer.svelte';
import ConnectorTab from './editors/ConnectorTab.svelte';
import OrganisationTab from './editors/OrganisationTab.svelte';
import ProjectTab from './editors/ProjectTab.svelte';
import UserTab from './editors/UserTab.svelte';
import McpTab from './editors/McpTab.svelte';
import TriggersExplorer from './nav/TriggersExplorer.svelte';
import ProcessesExplorer from './nav/ProcessesExplorer.svelte';
import AssistantTab from './assistant/AssistantTab.svelte';

import MethodologyTab from './editors/MethodologyTab.svelte';
import DomainTab from './editors/DomainTab.svelte';
import AlgorithmTab from './editors/AlgorithmTab.svelte';
import NodeTypeTab from './editors/NodeTypeTab.svelte';
import LinkTypeTab from './editors/LinkTypeTab.svelte';
import LifecycleTab from './editors/LifecycleTab.svelte';
import EnumTab from './editors/EnumTab.svelte';
import AdapterTab from './editors/AdapterTab.svelte';
import InstanceTab from './editors/InstanceTab.svelte';
import AgentTab from './editors/AgentTab.svelte';
import ActionTab from './editors/ActionTab.svelte';
import ConditionTab from './editors/ConditionTab.svelte';
import { walkSteps } from '../methodologyForm';
import GoalTab from './editors/GoalTab.svelte';
import ProcessTab from './editors/ProcessTab.svelte';
import MethodTab from './editors/MethodTab.svelte';
import StepTab from './editors/StepTab.svelte';
import GraphExplorerTab from './editors/GraphExplorerTab.svelte';
import RunTab from './editors/RunTab.svelte';
import ChangeTab from './editors/ChangeTab.svelte';
import PoliciesTab from './editors/PoliciesTab.svelte';
import ImportTab from './editors/ImportTab.svelte';

import EventsConsole from './bottom/EventsConsole.svelte';
import LogsConsole from './bottom/LogsConsole.svelte';
import ProblemsConsole from './bottom/ProblemsConsole.svelte';
import TokensConsole from './bottom/TokensConsole.svelte';

import TesterPanel from './right/TesterPanel.svelte';
import PropertiesPanel from './right/PropertiesPanel.svelte';
import DslHelpPanel from './right/DslHelpPanel.svelte';

// --- navigation (left) -------------------------------------------------------------

registerView({ id: 'methodologies', zone: 'left', title: 'Methodologies', icon: 'book', component: MethodologyExplorer, order: 1 });
registerView({ id: 'domains', zone: 'left', title: 'Domains', icon: 'graph', component: DomainExplorer, order: 1.5 });
registerView({ id: 'processes', zone: 'left', title: 'My processes', icon: 'runs', component: ProcessesExplorer, order: 2.2 });
registerView({ id: 'triggers', zone: 'left', title: 'Triggers', icon: 'clock', component: TriggersExplorer, order: 2.5 });
registerView({ id: 'baselines', zone: 'left', title: 'Baseline', icon: 'database', component: BaselineExplorer, editorArea: BaselineWorkspace, order: 3 });
registerView({
  id: 'changes',
  zone: 'left',
  title: 'Changes',
  icon: 'diff',
  component: ChangesExplorer,
  order: 2,
  badge: () => {
    let n = 0;
    // what waits for someone (stuck runs included): not the runs waiting for conditions another process establishes
    for (const p of processes.values())
      if ((p.status === 'waiting' && p.pending?.kind !== 'condition') || p.status === 'stuck' || p.status === 'clarifying') n++;
    return n || undefined;
  },
});
registerView({ id: 'peopleOrg', zone: 'left', title: 'People & Organisation', icon: 'user', component: PeopleOrgExplorer, order: 4.5 });
registerView({ id: 'adapters', zone: 'left', title: 'Adapters', icon: 'zap', component: AdaptersExplorer, order: 4.6 });
registerView({ id: 'access', zone: 'left', title: 'Access', icon: 'shield', component: AccessExplorer, order: 5 });

// --- console (bottom) ---------------------------------------------------------------------

registerView({ id: 'events', zone: 'bottom', title: 'Events', icon: 'radio', component: EventsConsole, order: 1 });
registerView({ id: 'logs', zone: 'bottom', title: 'Logs', icon: 'list', component: LogsConsole, order: 2 });
registerView({
  id: 'problems',
  zone: 'bottom',
  title: 'Issues',
  icon: 'alert',
  component: ProblemsConsole,
  order: 3,
  badge: () => {
    const d = activeDraft();
    return d && !d.readonly ? d.allIssues.length || undefined : undefined;
  },
});
registerView({
  id: 'tokens',
  zone: 'bottom',
  title: 'Tokens',
  icon: 'coins',
  component: TokensConsole,
  order: 4,
  badge: () => live.tokens.length || undefined,
});

// --- tools (right) ---------------------------------------------------------------------

registerView({ id: 'tester', zone: 'right', title: 'Test', icon: 'flask', component: TesterPanel, order: 1 });
registerView({ id: 'properties', zone: 'right', title: 'Properties', icon: 'info', component: PropertiesPanel, order: 2 });
registerView({ id: 'dsl', zone: 'right', title: 'DSL Help', icon: 'help', component: DslHelpPanel, order: 3 });

// --- editors ------------------------------------------------------------------------------

function discardGroup(tab: Tab) {
  const key = draftGroup(tab);
  const d = peekDraft(key);
  if (!d) return;
  if (d.isNew) drafts.delete(key);
  else d.revert();
}

const groupDirty = (tab: Tab) => peekDraft(draftGroup(tab))?.dirty ?? false;

registerView({
  id: 'methodology',
  zone: 'editor',
  title: 'Methodology',
  icon: 'book',
  component: MethodologyTab,
  key: (p) => (p.name ? `${p.name}@${p.version}` : 'new'),
  tabTitle: (t) => (t.params.name ? `${t.params.name} v${t.params.version}` : 'New methodology'),
  tooltip: (t) => (t.params.name ? `Methodology ${t.params.name} v${t.params.version}` : 'New methodology'),
  dirty: groupDirty,
  groupDirty,
  group: draftGroup,
  discard: discardGroup,
  properties: (t) => {
    const d = peekDraft(draftGroup(t));
    if (!d || d.isNew) return undefined;
    return {
      title: d.label,
      subtitle: 'Methodology',
      rows: [
        ['Status', d.status],
        ['Description', d.form.description],
        ['Agents', String(d.form.agents.length)],
        ['Actions', String(d.form.actions.length)],
        ['Conditions', String(d.form.conditions.length)],
        ['Goals', String(d.form.goals.length)],
        ['Issues', d.issues === null ? 'not validated' : String(d.allIssues.length)],
        ['Modified', `${formatDate(d.meta.updatedAt)}${d.meta.updatedBy ? ` by ${d.meta.updatedBy}` : ''}`],
        ['Published', formatDate(d.meta.publishedAt)],
      ],
    };
  },
});

const domainGroupDirty = (tab: Tab) => peekDomainDraft(domainGroup(tab))?.dirty ?? false;

function discardDomain(tab: Tab) {
  const key = domainGroup(tab);
  const d = peekDomainDraft(key);
  if (!d) return;
  if (d.isNew) domainDrafts.delete(key);
  else d.revert();
}

registerView({
  id: 'domain',
  zone: 'editor',
  title: 'Domain',
  icon: 'graph',
  component: DomainTab,
  key: (p) => (p.name ? `${p.name}@${p.version}` : 'new'),
  tabTitle: (t) => (t.params.name ? `${t.params.name} v${t.params.version} (domain)` : 'New domain'),
  tooltip: (t) => (t.params.name ? `Domain ${t.params.name} v${t.params.version}` : 'New domain'),
  dirty: domainGroupDirty,
  groupDirty: domainGroupDirty,
  group: domainGroup,
  discard: discardDomain,
  properties: (t) => {
    const d = peekDomainDraft(domainGroup(t));
    if (!d || d.isNew) return undefined;
    return {
      title: d.label,
      subtitle: 'Domain',
      rows: [
        ['Status', d.status],
        ['Description', d.form.description],
        ['Node types', String(d.form.nodeTypes.length)],
        ['Link types', String(d.form.linkTypes.length)],
        ['Enums', String(d.form.enums.length)],
        ['Lifecycles', String(d.form.lifecycles.length)],
        ['Algorithms', `${d.form.algorithms.length} (${d.form.instances.length} instances)`],
        ['Used by', String(d.usage.length)],
        ['Issues', d.issues === null ? 'not validated' : String(d.allIssues.length)],
        ['Modified', `${formatDate(d.meta.updatedAt)}${d.meta.updatedBy ? ` by ${d.meta.updatedBy}` : ''}`],
        ['Published', formatDate(d.meta.publishedAt)],
      ],
    };
  },
});

// node types, link types and lifecycles: one tab each, on the draft of their domain
const domainPartProps = (kind: 'nodetype' | 'linktype' | 'lifecycle' | 'enum') => (t: Tab) => {
  const d = peekDomainDraft(domainGroup(t));
  if (!d || d.isNew) return undefined;
  if (kind === 'nodetype') {
    const n = d.form.nodeTypes.find((x) => x.uid === t.params.uid);
    if (!n) return undefined;
    return {
      title: n.name || '(unnamed)',
      subtitle: 'Node type',
      rows: [
        ['Domain', d.label],
        ['Extends', n.extends],
        ['Lifecycle', n.lifecycle],
        ['Attributes', n.attributes.map((a) => a.name).join(', ')],
        ['Description', n.description],
      ] as [string, string][],
    };
  }
  if (kind === 'linktype') {
    const l = d.form.linkTypes.find((x) => x.uid === t.params.uid);
    if (!l) return undefined;
    return {
      title: l.name || '(unnamed)',
      subtitle: 'Link type',
      rows: [
        ['Domain', d.label],
        ['From', l.from],
        ['To', l.to],
        ['Composition', l.compose ? 'yes' : 'no'],
      ] as [string, string][],
    };
  }
  if (kind === 'enum') {
    const e = d.form.enums.find((x) => x.uid === t.params.uid);
    if (!e) return undefined;
    return {
      title: e.name || '(unnamed)',
      subtitle: 'Enum',
      rows: [
        ['Domain', d.label],
        ['Values', e.values.map((v) => v.value).join(', ')],
        ['Description', e.description],
      ] as [string, string][],
    };
  }
  const l = d.form.lifecycles.find((x) => x.uid === t.params.uid);
  if (!l) return undefined;
  return {
    title: l.name || '(unnamed)',
    subtitle: 'Lifecycle',
    rows: [
      ['Domain', d.label],
      ['Initial state', l.initial],
      ['States', l.states.map((s) => s.name).join(', ')],
      ['Transitions', l.transitions.map((x) => x.name).join(', ')],
    ] as [string, string][],
  };
};

for (const v of [
  { id: 'nodetype', title: 'Node type', icon: 'node', component: NodeTypeTab, param: 'nt', short: 'nt' },
  { id: 'linktype', title: 'Link type', icon: 'trace', component: LinkTypeTab, param: 'lt', short: 'lt' },
  { id: 'lifecycle', title: 'Lifecycle', icon: 'runs', component: LifecycleTab, param: 'lc', short: 'lc' },
  { id: 'enum', title: 'Enum', icon: 'tag', component: EnumTab, param: 'en', short: 'en' },
] as const) {
  registerView({
    id: v.id,
    zone: 'editor',
    title: v.title,
    icon: v.icon,
    component: v.component,
    key: (p) => `${p.name}@${p.version}/${v.short}:${p.uid}`,
    tabTitle: (t) => t.params[v.param] || '(unnamed)',
    tooltip: (t) => `${v.title} ${t.params[v.param] || ''} — ${t.params.name} v${t.params.version}`,
    dirty: domainGroupDirty,
    groupDirty: domainGroupDirty,
    group: domainGroup,
    discard: discardDomain,
    properties: domainPartProps(v.id),
  });
}

const algorithmProps = (kind: 'algorithm' | 'instance') => (t: Tab) => {
  const d = peekDomainDraft(domainGroup(t));
  if (!d || d.isNew) return undefined;
  if (kind === 'algorithm') {
    const a = d.form.algorithms.find((x) => x.uid === t.params.uid);
    if (!a) return undefined;
    return {
      title: a.name || '(unnamed)',
      subtitle: 'Algorithm',
      rows: [
        ['Domain', d.label],
        ['Type', a.type],
        ['Language', a.language],
        ['Parameters', a.params.map((p) => p.name).join(', ')],
        ['Description', a.description],
      ] as [string, string][],
    };
  }
  const i = d.form.instances.find((x) => x.uid === t.params.uid);
  if (!i) return undefined;
  return {
    title: i.name || '(unnamed)',
    subtitle: 'Algorithm instance',
    rows: [
      ['Domain', d.label],
      ['Algorithm', i.algorithm],
      ['Values', JSON.stringify(i.values)],
      ['Description', i.description],
    ] as [string, string][],
  };
};

registerView({
  id: 'algorithm',
  zone: 'editor',
  title: 'Algorithm',
  icon: 'code',
  component: AlgorithmTab,
  key: (p) => `${p.name}@${p.version}/alg:${p.uid}`,
  tabTitle: (t) => t.params.alg || '(unnamed)',
  tooltip: (t) => `Algorithm ${t.params.alg || ''} — ${t.params.name} v${t.params.version}`,
  dirty: domainGroupDirty,
  groupDirty: domainGroupDirty,
  group: domainGroup,
  discard: discardDomain,
  properties: algorithmProps('algorithm'),
});

registerView({
  id: 'adapter',
  zone: 'editor',
  title: 'Adapter',
  icon: 'zap',
  component: AdapterTab,
  key: (p) => p.name || 'new',
  tabTitle: (t) => (t.params.name ? `Adapter ${t.params.name}` : 'New adapter'),
});

registerView({
  id: 'instance',
  zone: 'editor',
  title: 'Algorithm instance',
  icon: 'tag',
  component: InstanceTab,
  key: (p) => `${p.name}@${p.version}/inst:${p.uid}`,
  tabTitle: (t) => t.params.inst || '(unnamed)',
  tooltip: (t) => `Algorithm instance ${t.params.inst || ''} — ${t.params.name} v${t.params.version}`,
  dirty: domainGroupDirty,
  groupDirty: domainGroupDirty,
  group: domainGroup,
  discard: discardDomain,
  properties: algorithmProps('instance'),
});

const ITEM_TITLES: Record<string, string> = { agent: 'Agent', action: 'Action', condition: 'Condition', goal: 'Goal', process: 'Process', method: 'Method' };
const ITEM_VIEWS = { agent: AgentTab, action: ActionTab, condition: ConditionTab, goal: GoalTab, process: ProcessTab, method: MethodTab };

for (const kind of ['agent', 'action', 'condition', 'goal', 'process', 'method'] as const) {
  const section = KIND_SECTION[kind];
  const itemOf = (t: Tab) => {
    const d = peekDraft(draftGroup(t));
    if (!d) return undefined;
    const i = d.indexOf(section, t.params.uid ?? '', t.params.name ?? '');
    return i >= 0 ? { d, item: d.items(section)[i] } : undefined;
  };
  registerView({
    id: kind,
    zone: 'editor',
    title: ITEM_TITLES[kind],
    icon: SECTION_ICON[section],
    component: ITEM_VIEWS[kind],
    key: (p) => `${p.m}@${p.v}/${p.uid}`,
    tabTitle: (t) => itemOf(t)?.item.name || t.params.name || '(unnamed)',
    tooltip: (t) => `${ITEM_TITLES[kind]} ${itemOf(t)?.item.name || t.params.name || ''} — ${t.params.m} v${t.params.v}`,
    dirty: (t) => {
      const x = itemOf(t);
      return x ? x.d.itemDirty(section, x.item.uid) : false;
    },
    groupDirty,
    group: draftGroup,
    discard: discardGroup,
    properties: (t) => {
      const x = itemOf(t);
      if (!x) return undefined;
      const it = x.item;
      const rows: [string, string][] = [
        ['Methodology', x.d.label],
        ['Description', it.description],
      ];
      if ('planner' in it && !('for' in it)) rows.push(['Planner', it.planner], ['Actions', it.actions.join(', ') || 'all'], ['Goals', it.goals.join(', ') || 'all']);
      if ('kind' in it) {
        rows.push(['Type', it.kind]);
        if (it.specializes) rows.push(['Specializes', it.specializes], ['Guard', it.when], ['Priority', String(it.priority)]);
        else rows.push(['Cost', String(it.cost)]);
        rows.push(['Permission', it.permission], ['Utility', it.utility]);
      }
      if ('expr' in it) rows.push(['Expression', it.expr]);
      if ('value' in it) rows.push(['Value', String(it.value)]);
      if ('for' in it) rows.push(['Capability', it.for], ['Planner', it.planner], ['Actions', it.actions.join(', ') || 'none'], ['Context', it.when || 'always'], ['Priority', String(it.priority)]);
      if ('steps' in it) rows.push(['Steps', String(walkSteps(it.steps).length)], ['References', String(it.references.length)]);
      return { title: it.name || '(unnamed)', subtitle: ITEM_TITLES[kind], rows };
    },
  });
}

const stepOf = (t: Tab) => {
  const d = peekDraft(draftGroup(t));
  const loc = d && findStep(d, t.params.skey ?? '');
  return d && loc ? { d, loc } : undefined;
};

registerView({
  id: 'step',
  zone: 'editor',
  title: 'Step',
  icon: 'node',
  component: StepTab,
  key: (p) => `${p.m}@${p.v}/step:${p.skey}`,
  tabTitle: (t) => stepOf(t)?.loc.step.name || t.params.name || '(unnamed)',
  tooltip: (t) => `Step ${t.params.name || ''} — ${t.params.m} v${t.params.v}`,
  dirty: (t) => {
    const x = stepOf(t);
    return x ? x.d.itemDirty(x.loc.section, x.loc.owner.uid) : false;
  },
  groupDirty,
  group: draftGroup,
  discard: discardGroup,
});

registerView({
  id: 'run',
  zone: 'editor',
  title: 'Run',
  icon: 'runs',
  component: RunTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => {
    const p = processes.get(t.params.id ?? '');
    return p?.title || (p?.agent ? `${p.agent} · ${shortId(p.id)}` : `Run ${shortId(t.params.id)}`);
  },
  tabIcon: (t) => (processes.get(t.params.id ?? '')?.parentId ? 'bot' : 'runs'),
  tooltip: (t) => {
    const p = processes.get(t.params.id ?? '');
    return `Run ${t.params.id}${p ? ` — ${p.status}` : ''}`;
  },
  properties: (t) => {
    const p = processes.get(t.params.id ?? '');
    if (!p) return undefined;
    return {
      title: p.title || `Run ${shortId(p.id)}`,
      subtitle: 'Run',
      rows: [
        ['Id', p.id ?? ''],
        ['Status', p.status ?? ''],
        ['Methodology', p.methodology ?? ''],
        ['Agent', p.agent ?? ''],
        ['Planner', p.planner ?? ''],
        ['Goal', p.goal ?? ''],
        ['Triggered by', p.trigger ?? ''],
        ['Tokens (input / output)', `${formatInt(p.usage?.inputTokens)} / ${formatInt(p.usage?.outputTokens)}`],
        ['Trace', p.traceId ?? ''],
        ['Created', formatDate(p.createdAt)],
      ],
    };
  },
});

registerView({
  id: 'change',
  zone: 'editor',
  title: 'Change',
  icon: 'diff',
  component: ChangeTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => changes.items.find((c) => c.id === t.params.id)?.title || `Change ${shortId(t.params.id)}`,
});

registerView({
  id: 'connector',
  zone: 'editor',
  title: 'Connector',
  icon: 'zap',
  component: ConnectorTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => `Connector ${t.params.id}`,
});

registerView({
  id: 'mcp',
  zone: 'editor',
  title: 'MCP',
  icon: 'book',
  component: McpTab,
  key: (p) => p.name || 'new',
  tabTitle: (t) => (t.params.name ? `MCP ${t.params.name}` : 'New MCP'),
});

registerView({
  id: 'unit',
  zone: 'editor',
  title: 'Organisation',
  icon: 'user',
  component: OrganisationTab,
  key: (p) => p.key ?? '',
  tabTitle: (t) => t.params.key ?? 'Organisation',
});

registerView({
  id: 'project',
  zone: 'editor',
  title: 'Project',
  icon: 'diff',
  component: ProjectTab,
  key: (p) => p.key ?? '',
  tabTitle: (t) => t.params.key ?? 'Project',
});

registerView({
  id: 'user',
  zone: 'editor',
  title: 'User',
  icon: 'user',
  component: UserTab,
  key: (p) => p.key ?? '',
  tabTitle: (t) => t.params.key ?? 'User',
});

registerView({
  id: 'node',
  zone: 'editor',
  title: 'Node',
  icon: 'node',
  component: NodeTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => t.params.key || `Node ${shortId(t.params.id)}`,
});

registerView({
  id: 'policies',
  zone: 'editor',
  title: 'Access',
  icon: 'shield',
  component: PoliciesTab,
  key: () => 'all',
  tabTitle: () => 'Access',
});

registerView({
  id: 'graphExplorer',
  zone: 'editor',
  title: 'Graph explorer',
  icon: 'graph',
  component: GraphExplorerTab,
  key: () => 'main',
  tabTitle: () => 'Graph explorer',
});

registerView({
  id: 'assistant',
  zone: 'editor',
  title: 'Assistant',
  icon: 'chat',
  component: AssistantTab,
  key: () => 'main',
  tabTitle: () => 'Assistant',
});

registerView({
  id: 'import',
  zone: 'editor',
  title: 'Import',
  icon: 'upload',
  component: ImportTab,
  key: () => 'yaml',
  tabTitle: () => 'Import YAML',
});
