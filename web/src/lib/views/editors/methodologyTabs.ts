// Onglets liés à un brouillon de méthodologie : ouverture des éléments,
// actions communes de la barre d'outils, résolution des problèmes.
import type { IconName } from '../../shell/Icon.svelte';
import type { Tab, TabSpec, ToolbarAction } from '../../shell/types';
import { openTab, closeWhere, replaceTab } from '../../shell/tabs.svelte';
import { notify, requestReveal } from '../../shell/workbench.svelte';
import { editorView } from '../../shell/registry';
import { tabId } from '../../shell/tabs.svelte';
import { getDraft, type Draft } from '../../stores/drafts.svelte';
import type { Section, SectionItem } from '../../methodologyForm';

export const SECTION_KIND: Record<Section, string> = {
  agents: 'agent',
  actions: 'action',
  conditions: 'condition',
  goals: 'goal',
};

export const KIND_SECTION: Record<string, Section> = {
  agent: 'agents',
  action: 'actions',
  condition: 'conditions',
  goal: 'goals',
};

export const SECTION_LABEL: Record<Section, string> = {
  agents: 'Agents',
  actions: 'Actions',
  conditions: 'Conditions',
  goals: 'Objectifs',
};

export const SECTION_ICON: Record<Section, IconName> = {
  agents: 'bot',
  actions: 'zap',
  conditions: 'branch',
  goals: 'target',
};

/** Nom d'un élément au singulier, pour les messages. */
export const SECTION_SINGULAR: Record<Section, string> = {
  agents: "l'agent",
  actions: "l'action",
  conditions: 'la condition',
  goals: "l'objectif",
};

export function methodologySpec(name: string, version: string): TabSpec {
  return { kind: 'methodology', params: { name, version } };
}

export function itemSpec(d: Draft, section: Section, item: SectionItem): TabSpec {
  return { kind: SECTION_KIND[section], params: { m: d.name, v: d.version, uid: item.uid, name: item.name } };
}

export function openItem(d: Draft, section: Section, item: SectionItem, pin = false): void {
  openTab(itemSpec(d, section, item), { pin });
}

/** Brouillon d'un onglet (méthodologie ou élément). */
export function draftOf(tab: Tab): Draft {
  if (tab.kind === 'methodology') return getDraft(tab.params.name ?? '', tab.params.version ?? '');
  return getDraft(tab.params.m ?? '', tab.params.v ?? '');
}

/** Clé du groupe d'onglets partageant le brouillon. */
export function draftGroup(tab: Tab): string {
  if (tab.kind === 'methodology') return tab.params.name ? `${tab.params.name}@${tab.params.version}` : 'new';
  return `${tab.params.m}@${tab.params.v}`;
}

/** Élément édité par un onglet (identifiant local, sinon nom). */
export function resolveItem(tab: Tab): { draft: Draft; section: Section; index: number; item: SectionItem | undefined } {
  const draft = draftOf(tab);
  const section = KIND_SECTION[tab.kind];
  const index = draft.indexOf(section, tab.params.uid ?? '', tab.params.name ?? '');
  return { draft, section, index, item: index >= 0 ? draft.items(section)[index] : undefined };
}

/** Si l'élément a été retrouvé par son nom, l'onglet reprend son identifiant local. */
export function syncTabUid(tab: Tab, section: Section, item: SectionItem | undefined): void {
  if (!item) return;
  if (tab.params.name !== item.name) tab.params.name = item.name;
  if (tab.params.uid !== item.uid) {
    const spec: TabSpec = { kind: tab.kind, params: { ...tab.params, uid: item.uid } };
    if (tabId(spec) !== tab.id) replaceTab(tab.id, spec);
  }
}

// --- actions --------------------------------------------------------------------------

async function save(d: Draft): Promise<void> {
  const res = await d.save();
  if (res) notify(`${d.label} enregistrée.`, 'ok');
  else if (d.error) notify(d.error, 'error');
}

/** Actions communes des onglets d'un brouillon. */
export function draftActions(d: Draft, extra: ToolbarAction[] = []): ToolbarAction[] {
  const busy = !!d.busy || d.loading;
  const acts: ToolbarAction[] = [];
  if (!d.readonly) {
    acts.push({
      id: 'save',
      label: d.busy === 'save' ? 'Enregistrement…' : 'Enregistrer',
      icon: 'save',
      primary: true,
      shortcut: 'Ctrl+S',
      disabled: busy || (!d.dirty && !d.isNew),
      run: () => save(d),
    });
    if (!d.isNew) {
      acts.push({
        id: 'validate',
        label: d.busy === 'validate' ? 'Validation…' : 'Valider',
        icon: 'check',
        disabled: busy,
        run: async () => {
          await d.validate();
          if (d.error) notify(d.error, 'error');
          else notify(d.allIssues.length ? `${d.allIssues.length} problème(s) — voir la console « Problèmes ».` : 'Aucun problème détecté.', d.allIssues.length ? 'info' : 'ok');
        },
      });
      acts.push({
        id: 'publish',
        label: d.busy === 'publish' ? 'Publication…' : 'Publier',
        icon: 'upload',
        disabled: !d.canPublish,
        title: d.canPublish ? 'Figer cette version' : 'Enregistrez et validez le brouillon (sans problème) pour pouvoir le publier',
        run: async () => {
          if (await d.publish()) notify(`${d.label} publiée.`, 'ok');
          else if (d.error) notify(d.error, 'error');
        },
      });
    }
  }
  if (!d.isNew) {
    acts.push({ id: 'export', label: 'Exporter', icon: 'download', title: 'Exporter en YAML', disabled: busy, run: () => d.exportYaml() });
    acts.push({
      id: 'version',
      label: 'Nouvelle version',
      icon: 'copy',
      disabled: busy,
      run: async () => {
        const v = await d.newVersion();
        if (v) openTab(methodologySpec(d.name, v), { pin: true });
        else if (d.error) notify(d.error, 'error');
      },
    });
    acts.push({
      id: 'reload',
      label: 'Recharger',
      icon: 'refresh',
      disabled: busy,
      title: 'Recharger depuis le registre (abandonne les modifications)',
      run: () => {
        if (d.dirty && !confirm('Abandonner les modifications non enregistrées et recharger ?')) return;
        void d.reload();
      },
    });
  }
  acts.push(...extra);
  if (!d.isNew && d.status !== 'archived') {
    acts.push({
      id: 'delete',
      label: d.status === 'draft' ? 'Supprimer la version' : 'Archiver',
      icon: 'trash',
      danger: true,
      disabled: busy,
      run: async () => {
        const r = await d.remove();
        if (r === 'deleted') {
          closeWhere((t) => ['methodology', 'agent', 'action', 'condition', 'goal'].includes(t.kind) && draftGroup(t) === d.key);
          notify(`${d.label} supprimée.`, 'ok');
        } else if (r === 'archived') notify(`${d.label} archivée.`, 'ok');
        else if (d.error) notify(d.error, 'error');
      },
    });
  }
  return acts;
}

// --- problèmes ------------------------------------------------------------------------------

/** Ouvre l'onglet concerné par un chemin de problème et met le champ en évidence. */
export function revealIssue(d: Draft, path: string): void {
  const m = /^(agents|actions|conditions|goals)\[(\d+)\]/.exec(path);
  let spec: TabSpec = methodologySpec(d.name, d.version);
  if (m) {
    const section = m[1] as Section;
    const item = d.items(section)[Number(m[2])];
    if (item) spec = itemSpec(d, section, item);
  }
  const tab = openTab(spec);
  requestReveal(tab.id, path);
}

export function isDraftTab(tab: Tab | undefined): boolean {
  return !!tab && !!editorView(tab.kind) && ['methodology', 'agent', 'action', 'condition', 'goal'].includes(tab.kind);
}

/** Action « Supprimer <élément> » des onglets d'éléments. */
export function removeItemAction(d: Draft, section: Section, tab: Tab, index: () => number): ToolbarAction[] {
  if (d.readonly) return [];
  return [
    {
      id: 'removeItem',
      label: `Supprimer ${SECTION_SINGULAR[section]}`,
      icon: 'trash',
      danger: true,
      disabled: index() < 0,
      run: () => {
        const i = index();
        const item = d.items(section)[i];
        if (!item || !confirm(`Supprimer ${SECTION_SINGULAR[section]} « ${item.name || 'sans nom'} » du brouillon ?`)) return;
        d.form[section].splice(i, 1);
        closeWhere((t) => t.id === tab.id);
      },
    },
  ];
}
