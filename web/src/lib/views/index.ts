// Registration of the workbench's views in the shell's registry.
import { registerView } from '../shell/registry';
import type { Tab } from '../shell/types';
import { formatDate, formatInt, shortId } from '../api';
import { peekDraft, drafts } from '../stores/drafts.svelte';
import { peekDomainDraft, domainDrafts } from '../stores/domains.svelte';
import { processes, live } from '../stores/live.svelte';
import { baselines, changes } from '../stores/catalog.svelte';
import NodeTab from './editors/NodeTab.svelte';
import { draftGroup, KIND_SECTION, SECTION_ICON } from './editors/methodologyTabs';
import { activeDraft } from './bottom/activeDraft';
import { domainGroup } from './editors/domainTabs';

import MethodologyExplorer from './nav/MethodologyExplorer.svelte';
import DomainExplorer from './nav/DomainExplorer.svelte';
import AlgorithmExplorer from './nav/AlgorithmExplorer.svelte';
import BaselineExplorer from './nav/BaselineExplorer.svelte';
import ChangesExplorer from './nav/ChangesExplorer.svelte';
import TokensTab from './dashboard/TokensTab.svelte';
import PlatformTab from './platform/PlatformTab.svelte';
import AccessExplorer from './nav/AccessExplorer.svelte';
import OrganisationExplorer from './nav/OrganisationExplorer.svelte';
import ToolsExplorer from './nav/ToolsExplorer.svelte';
import ConnectorTab from './editors/ConnectorTab.svelte';
import McpTab from './editors/McpTab.svelte';
import AdapterTab from './editors/AdapterTab.svelte';
import BindingTab from './editors/BindingTab.svelte';
import TriggersExplorer from './nav/TriggersExplorer.svelte';
import AssistantPanel from './assistant/AssistantPanel.svelte';
import AssistantTab from './assistant/AssistantTab.svelte';

import MethodologyTab from './editors/MethodologyTab.svelte';
import DomainTab from './editors/DomainTab.svelte';
import AlgorithmTab from './editors/AlgorithmTab.svelte';
import InstanceTab from './editors/InstanceTab.svelte';
import AgentTab from './editors/AgentTab.svelte';
import ActionTab from './editors/ActionTab.svelte';
import ConditionTab from './editors/ConditionTab.svelte';
import GoalTab from './editors/GoalTab.svelte';
import RunTab from './editors/RunTab.svelte';
import ChangeTab from './editors/ChangeTab.svelte';
import JournalTab from './editors/JournalTab.svelte';
import BaselineTab from './editors/BaselineTab.svelte';
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

registerView({ id: 'assistant', zone: 'left', title: 'Assistant', icon: 'chat', component: AssistantPanel, order: 0 });
registerView({ id: 'methodologies', zone: 'left', title: 'Methodologies', icon: 'book', component: MethodologyExplorer, order: 1 });
registerView({ id: 'domains', zone: 'left', title: 'Domains', icon: 'graph', component: DomainExplorer, order: 1.5 });
registerView({ id: 'algorithms', zone: 'left', title: 'Algorithms', icon: 'code', component: AlgorithmExplorer, order: 1.6 });
registerView({ id: 'triggers', zone: 'left', title: 'Triggers', icon: 'clock', component: TriggersExplorer, order: 2.5 });
registerView({ id: 'baselines', zone: 'left', title: 'Baseline', icon: 'database', component: BaselineExplorer, order: 3 });
registerView({
  id: 'changes',
  zone: 'left',
  title: 'Changes',
  icon: 'diff',
  component: ChangesExplorer,
  order: 2,
  badge: () => {
    let n = 0;
    for (const p of processes.values()) if (p.status === 'waiting' || p.status === 'clarifying') n++;
    return n || undefined;
  },
});
registerView({ id: 'organisation', zone: 'left', title: 'Organisation', icon: 'user', component: OrganisationExplorer, order: 4.5 });
registerView({ id: 'tools', zone: 'left', title: 'Tools', icon: 'zap', component: ToolsExplorer, order: 4.7 });
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
        ['Algorithms', `${d.form.algorithms.length} (${d.form.instances.length} instances)`],
        ['Used by', String(d.usage.length)],
        ['Issues', d.issues === null ? 'not validated' : String(d.allIssues.length)],
        ['Modified', `${formatDate(d.meta.updatedAt)}${d.meta.updatedBy ? ` by ${d.meta.updatedBy}` : ''}`],
        ['Published', formatDate(d.meta.publishedAt)],
      ],
    };
  },
});

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

const ITEM_TITLES: Record<string, string> = { agent: 'Agent', action: 'Action', condition: 'Condition', goal: 'Goal' };
const ITEM_VIEWS = { agent: AgentTab, action: ActionTab, condition: ConditionTab, goal: GoalTab };

for (const kind of ['agent', 'action', 'condition', 'goal'] as const) {
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
      if ('planner' in it) rows.push(['Planner', it.planner], ['Actions', it.actions.join(', ') || 'all'], ['Goals', it.goals.join(', ') || 'all']);
      if ('kind' in it) {
        rows.push(['Type', it.kind]);
        if (it.specializes) rows.push(['Specializes', it.specializes], ['Guard', it.when], ['Priority', String(it.priority)]);
        else rows.push(['Cost', String(it.cost)]);
        rows.push(['Permission', it.permission], ['Utility', it.utility]);
      }
      if ('expr' in it) rows.push(['Expression', it.expr]);
      if ('value' in it) rows.push(['Value', String(it.value)]);
      return { title: it.name || '(unnamed)', subtitle: ITEM_TITLES[kind], rows };
    },
  });
}

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
  id: 'journal',
  zone: 'editor',
  title: 'Execution journal',
  icon: 'list',
  component: JournalTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => {
    const c = changes.items.find((x) => x.id === t.params.id);
    return `Journal · ${c?.title || shortId(t.params.id)}`;
  },
  tooltip: (t) => `Execution journal of change ${t.params.id}`,
});

registerView({
  id: 'baseline',
  zone: 'editor',
  title: 'Baseline',
  icon: 'database',
  component: BaselineTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => baselines.items.find((b) => b.id === t.params.id)?.name || `Baseline ${shortId(t.params.id)}`,
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
  id: 'adapter',
  zone: 'editor',
  title: 'Adapter',
  icon: 'branch',
  component: AdapterTab,
  key: (p) => `${p.mcp}/${p.connector || 'new'}`,
  tabTitle: (t) => (t.params.connector ? `${t.params.mcp} via ${t.params.connector}` : `New adapter (${t.params.mcp})`),
});

registerView({
  id: 'binding',
  zone: 'editor',
  title: 'Binding',
  icon: 'key',
  component: BindingTab,
  key: (p) => `${p.org}/${p.mcp || 'new'}`,
  tabTitle: (t) => (t.params.mcp ? `${t.params.org}: ${t.params.mcp}` : `New binding (${t.params.org})`),
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
  title: 'Policies',
  icon: 'shield',
  component: PoliciesTab,
  key: () => 'all',
  tabTitle: () => 'Access policies',
});

registerView({
  id: 'tokenUsage',
  zone: 'editor',
  title: 'Token usage',
  icon: 'coins',
  component: TokensTab,
  key: () => 'main',
  tabTitle: () => 'Token usage',
});

registerView({
  id: 'platform',
  zone: 'editor',
  title: 'Platform settings',
  icon: 'settings',
  component: PlatformTab,
  key: () => 'main',
  tabTitle: () => 'Platform settings',
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
