// Registration of the workbench's views in the shell's registry.
import { registerView } from '../shell/registry';
import type { Tab } from '../shell/types';
import { shortId } from '../api';
import { peekDraft, drafts } from '../stores/drafts.svelte';
import { peekDomainDraft, domainDrafts } from '../stores/domains.svelte';
import { processes } from '../stores/live.svelte';
import { changes } from '../stores/catalog.svelte';
import NodeTab from './editors/NodeTab.svelte';
import { draftGroup, findStep, KIND_SECTION, SECTION_ICON } from './editors/methodologyTabs';
import { activeDraft } from './bottom/activeDraft';
import { domainGroup } from './editors/domainTabs';
import { actionHelp, adapterHelp, algorithmHelp, domainHelp, instanceHelp, methodologyHelp } from './editors/dslTargets';
import './nodeEditors';

import MethodologyExplorer from './nav/MethodologyExplorer.svelte';
import DomainExplorer from './nav/DomainExplorer.svelte';
import BaselineExplorer from './nav/BaselineExplorer.svelte';
import ChangesExplorer from './nav/ChangesExplorer.svelte';
import AccessExplorer from './nav/AccessExplorer.svelte';
import PeopleOrgExplorer from './nav/PeopleOrgExplorer.svelte';
import AdaptersExplorer from './nav/AdaptersExplorer.svelte';
import StatsExplorer from './nav/StatsExplorer.svelte';
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
import ItemTypeTab from './editors/ItemTypeTab.svelte';
import AdapterTab from './editors/AdapterTab.svelte';
import InstanceTab from './editors/InstanceTab.svelte';
import AgentTab from './editors/AgentTab.svelte';
import ActionTab from './editors/ActionTab.svelte';
import ConditionTab from './editors/ConditionTab.svelte';
import GoalTab from './editors/GoalTab.svelte';
import ProcessTab from './editors/ProcessTab.svelte';
import MethodTab from './editors/MethodTab.svelte';
import StepTab from './editors/StepTab.svelte';
import GraphExplorerTab from './editors/GraphExplorerTab.svelte';
import RunTab from './editors/RunTab.svelte';
import ChangeTab from './editors/ChangeTab.svelte';
import PoliciesTab from './editors/PoliciesTab.svelte';
import ImportTab from './editors/ImportTab.svelte';
import SourceImportTab from './editors/SourceImportTab.svelte';
import JobsTab from './editors/JobsTab.svelte';
import GraphStatsTab from './editors/GraphStatsTab.svelte';
import BaselineTab from './editors/BaselineTab.svelte';
import { importKind, IMPORT_KINDS } from './editors/importKinds';

import EventsConsole from './bottom/EventsConsole.svelte';
import LogsConsole from './bottom/LogsConsole.svelte';
import ProblemsConsole from './bottom/ProblemsConsole.svelte';
import TokensConsole from './bottom/TokensConsole.svelte';

import TesterPanel from './right/TesterPanel.svelte';
import { issueBadge } from '../issues';
import DslHelpPanel from './right/DslHelpPanel.svelte';

// --- navigation (left) -------------------------------------------------------------

registerView({ id: 'methodologies', zone: 'left', title: 'Methodologies', icon: 'book', component: MethodologyExplorer, order: 1 });
registerView({ id: 'domains', zone: 'left', title: 'Domains', icon: 'graph', component: DomainExplorer, order: 1.5 });
registerView({ id: 'processes', zone: 'left', title: 'My processes', icon: 'runs', component: ProcessesExplorer, order: 2.2 });
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
    // what waits for someone (stuck runs included): not the runs waiting for conditions another process establishes
    for (const p of processes.values())
      if ((p.status === 'waiting' && p.pending?.kind !== 'condition') || p.status === 'stuck' || p.status === 'clarifying') n++;
    return n || undefined;
  },
});
registerView({ id: 'stats', zone: 'left', title: 'Graph statistics', icon: 'chart', component: StatsExplorer, order: 3.5 });
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
    return d && !d.readonly ? issueBadge(d.allIssues) : undefined;
  },
});
registerView({
  id: 'tokens',
  zone: 'bottom',
  title: 'Tokens',
  icon: 'coins',
  component: TokensConsole,
  order: 4,
});

// --- tools (right) ---------------------------------------------------------------------

registerView({ id: 'tester', zone: 'right', title: 'Test', icon: 'flask', component: TesterPanel, order: 1 });
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
  help: methodologyHelp,
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
  help: domainHelp,
});


for (const v of [
  { id: 'nodetype', title: 'Node type', icon: 'node', component: NodeTypeTab, param: 'nt', short: 'nt' },
  { id: 'linktype', title: 'Link type', icon: 'trace', component: LinkTypeTab, param: 'lt', short: 'lt' },
  { id: 'lifecycle', title: 'Lifecycle', icon: 'runs', component: LifecycleTab, param: 'lc', short: 'lc' },
  { id: 'enum', title: 'Enum', icon: 'tag', component: EnumTab, param: 'en', short: 'en' },
  { id: 'itemtype', title: 'Item type', icon: 'tag', component: ItemTypeTab, param: 'it', short: 'it' },
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
  });
}


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
  help: algorithmHelp,
});

registerView({
  id: 'adapter',
  zone: 'editor',
  title: 'Adapter',
  icon: 'zap',
  component: AdapterTab,
  key: (p) => p.name || 'new',
  tabTitle: (t) => (t.params.name ? `Adapter ${t.params.name}` : 'New adapter'),
  help: adapterHelp,
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
  help: instanceHelp,
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
    help: kind === 'action' ? actionHelp : undefined,
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
  id: 'baseline',
  zone: 'editor',
  title: 'Baseline',
  icon: 'database',
  component: BaselineTab,
  // one tab per branch; the baseline it shows is the tab's own state
  key: (p) => `${p.namespace ?? ''}/${p.branch || 'main'}`,
  pinned: true,
  tabTitle: (t) => `${t.params.namespace} · ${t.params.branch || 'main'}`,
  tabIcon: () => 'database',
  tooltip: (t) => `Baseline of ${t.params.namespace}, branch ${t.params.branch || 'main'}${t.params.baseline ? ` at ${shortId(t.params.baseline)}` : ' (head)'}`,
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
  key: (p) => importKind(p.what),
  tabTitle: (t) => IMPORT_KINDS[importKind(t.params.what)].title,
});

// the sources a domain declares (ADR 0148), run by their importer services
registerView({
  id: 'sources',
  zone: 'editor',
  title: 'Import from a source',
  icon: 'database',
  component: SourceImportTab,
  key: (p) => p.ns ?? '',
  tabTitle: (t) => (t.params.ns ? `Import into ${t.params.ns}` : 'Import from a source'),
});

// the long runs of the person (ADR 0149), live from the job stream
registerView({
  id: 'graphStats',
  zone: 'editor',
  title: 'Statistics',
  icon: 'chart',
  component: GraphStatsTab,
  key: (p) => p.namespace ?? '',
  tabTitle: (t) => `Stats ${t.params.namespace ?? ''}`,
  tooltip: (t) => `Statistics of the graph of ${t.params.namespace ?? ''}`,
});
registerView({
  id: 'jobs',
  zone: 'editor',
  title: 'Jobs',
  icon: 'clock',
  component: JobsTab,
  key: () => 'main',
  tabTitle: () => 'Jobs',
});
