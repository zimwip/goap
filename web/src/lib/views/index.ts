// Enregistrement des vues de l'atelier dans le registre du shell.
import { registerView } from '../shell/registry';
import type { Tab } from '../shell/types';
import { formatDate, formatInt, shortId } from '../api';
import { peekDraft, drafts } from '../stores/drafts.svelte';
import { processes, live } from '../stores/live.svelte';
import { baselines, changes } from '../stores/catalog.svelte';
import { draftGroup, KIND_SECTION, SECTION_ICON } from './editors/methodologyTabs';
import { activeDraft } from './bottom/activeDraft';

import MethodologyExplorer from './nav/MethodologyExplorer.svelte';
import RunsExplorer from './nav/RunsExplorer.svelte';
import BaselineExplorer from './nav/BaselineExplorer.svelte';
import ChangesExplorer from './nav/ChangesExplorer.svelte';
import AccessExplorer from './nav/AccessExplorer.svelte';
import TriggersExplorer from './nav/TriggersExplorer.svelte';
import AssistantPanel from './assistant/AssistantPanel.svelte';
import AssistantTab from './assistant/AssistantTab.svelte';

import MethodologyTab from './editors/MethodologyTab.svelte';
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

// --- navigation (gauche) -------------------------------------------------------------

registerView({ id: 'assistant', zone: 'left', title: 'Assistant', icon: 'chat', component: AssistantPanel, order: 0 });
registerView({ id: 'methodologies', zone: 'left', title: 'Méthodologies', icon: 'book', component: MethodologyExplorer, order: 1 });
registerView({
  id: 'runs',
  zone: 'left',
  title: 'Exécutions',
  icon: 'runs',
  component: RunsExplorer,
  order: 2,
  badge: () => {
    let n = 0;
    for (const p of processes.values()) if (p.status === 'waiting' || p.status === 'clarifying') n++;
    return n || undefined;
  },
});
registerView({ id: 'triggers', zone: 'left', title: 'Déclencheurs', icon: 'clock', component: TriggersExplorer, order: 2.5 });
registerView({ id: 'baselines', zone: 'left', title: 'Référentiel', icon: 'database', component: BaselineExplorer, order: 3 });
registerView({ id: 'changes', zone: 'left', title: 'Changements', icon: 'diff', component: ChangesExplorer, order: 4 });
registerView({ id: 'access', zone: 'left', title: 'Accès', icon: 'shield', component: AccessExplorer, order: 5 });

// --- console (bas) ---------------------------------------------------------------------

registerView({ id: 'events', zone: 'bottom', title: 'Événements', icon: 'radio', component: EventsConsole, order: 1 });
registerView({ id: 'logs', zone: 'bottom', title: 'Journaux', icon: 'list', component: LogsConsole, order: 2 });
registerView({
  id: 'problems',
  zone: 'bottom',
  title: 'Problèmes',
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

// --- outils (droite) ---------------------------------------------------------------------

registerView({ id: 'tester', zone: 'right', title: 'Tester', icon: 'flask', component: TesterPanel, order: 1 });
registerView({ id: 'properties', zone: 'right', title: 'Propriétés', icon: 'info', component: PropertiesPanel, order: 2 });
registerView({ id: 'dsl', zone: 'right', title: 'Aide DSL', icon: 'help', component: DslHelpPanel, order: 3 });

// --- éditeurs ------------------------------------------------------------------------------

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
  title: 'Méthodologie',
  icon: 'book',
  component: MethodologyTab,
  key: (p) => (p.name ? `${p.name}@${p.version}` : 'new'),
  tabTitle: (t) => (t.params.name ? `${t.params.name} v${t.params.version}` : 'Nouvelle méthodologie'),
  tooltip: (t) => (t.params.name ? `Méthodologie ${t.params.name} v${t.params.version}` : 'Nouvelle méthodologie'),
  dirty: groupDirty,
  groupDirty,
  group: draftGroup,
  discard: discardGroup,
  properties: (t) => {
    const d = peekDraft(draftGroup(t));
    if (!d || d.isNew) return undefined;
    return {
      title: d.label,
      subtitle: 'Méthodologie',
      rows: [
        ['Statut', d.status],
        ['Description', d.form.description],
        ['Agents', String(d.form.agents.length)],
        ['Actions', String(d.form.actions.length)],
        ['Conditions', String(d.form.conditions.length)],
        ['Objectifs', String(d.form.goals.length)],
        ['Problèmes', d.issues === null ? 'non validé' : String(d.allIssues.length)],
        ['Modifiée', `${formatDate(d.meta.updatedAt)}${d.meta.updatedBy ? ` par ${d.meta.updatedBy}` : ''}`],
        ['Publiée', formatDate(d.meta.publishedAt)],
      ],
    };
  },
});

const ITEM_TITLES: Record<string, string> = { agent: 'Agent', action: 'Action', condition: 'Condition', goal: 'Objectif' };
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
    tabTitle: (t) => itemOf(t)?.item.name || t.params.name || '(sans nom)',
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
        ['Méthodologie', x.d.label],
        ['Description', it.description],
      ];
      if ('planner' in it) rows.push(['Planificateur', it.planner], ['Actions', it.actions.join(', ') || 'toutes'], ['Objectifs', it.goals.join(', ') || 'tous']);
      if ('kind' in it) {
        rows.push(['Type', it.kind]);
        if (it.specializes) rows.push(['Spécialise', it.specializes], ['Garde', it.when], ['Priorité', String(it.priority)]);
        else rows.push(['Coût', String(it.cost)]);
        rows.push(['Permission', it.permission], ['Utilité', it.utility]);
      }
      if ('expr' in it) rows.push(['Expression', it.expr]);
      if ('value' in it) rows.push(['Valeur', String(it.value)]);
      return { title: it.name || '(sans nom)', subtitle: ITEM_TITLES[kind], rows };
    },
  });
}

registerView({
  id: 'run',
  zone: 'editor',
  title: 'Exécution',
  icon: 'runs',
  component: RunTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => {
    const p = processes.get(t.params.id ?? '');
    return p?.title || (p?.agent ? `${p.agent} · ${shortId(p.id)}` : `Exécution ${shortId(t.params.id)}`);
  },
  tabIcon: (t) => (processes.get(t.params.id ?? '')?.parentId ? 'bot' : 'runs'),
  tooltip: (t) => {
    const p = processes.get(t.params.id ?? '');
    return `Exécution ${t.params.id}${p ? ` — ${p.status}` : ''}`;
  },
  properties: (t) => {
    const p = processes.get(t.params.id ?? '');
    if (!p) return undefined;
    return {
      title: p.title || `Exécution ${shortId(p.id)}`,
      subtitle: 'Exécution',
      rows: [
        ['Identifiant', p.id ?? ''],
        ['Statut', p.status ?? ''],
        ['Méthodologie', p.methodology ?? ''],
        ['Agent', p.agent ?? ''],
        ['Planificateur', p.planner ?? ''],
        ['Objectif', p.goal ?? ''],
        ['Déclenché par', p.trigger ?? ''],
        ['Tokens (entrée / sortie)', `${formatInt(p.usage?.inputTokens)} / ${formatInt(p.usage?.outputTokens)}`],
        ['Trace', p.traceId ?? ''],
        ['Créé', formatDate(p.createdAt)],
      ],
    };
  },
});

registerView({
  id: 'change',
  zone: 'editor',
  title: 'Changement',
  icon: 'diff',
  component: ChangeTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => changes.items.find((c) => c.id === t.params.id)?.title || `Changement ${shortId(t.params.id)}`,
});

registerView({
  id: 'journal',
  zone: 'editor',
  title: "Journal d'exécution",
  icon: 'list',
  component: JournalTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => {
    const c = changes.items.find((x) => x.id === t.params.id);
    return `Journal · ${c?.title || shortId(t.params.id)}`;
  },
  tooltip: (t) => `Journal d'exécution du changement ${t.params.id}`,
});

registerView({
  id: 'baseline',
  zone: 'editor',
  title: 'Référentiel',
  icon: 'database',
  component: BaselineTab,
  key: (p) => p.id ?? '',
  tabTitle: (t) => baselines.items.find((b) => b.id === t.params.id)?.name || `Référentiel ${shortId(t.params.id)}`,
});

registerView({
  id: 'policies',
  zone: 'editor',
  title: 'Politiques',
  icon: 'shield',
  component: PoliciesTab,
  key: () => 'all',
  tabTitle: () => "Politiques d'accès",
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
  title: 'Importer',
  icon: 'upload',
  component: ImportTab,
  key: () => 'yaml',
  tabTitle: () => 'Importer YAML',
});
