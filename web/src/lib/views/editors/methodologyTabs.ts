// Tabs tied to a methodology draft: opening elements, common toolbar
// actions, resolving issues.
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
  goals: 'Goals',
};

export const SECTION_ICON: Record<Section, IconName> = {
  agents: 'bot',
  actions: 'zap',
  conditions: 'branch',
  goals: 'target',
};

/** Singular name of an element, for messages. */
export const SECTION_SINGULAR: Record<Section, string> = {
  agents: 'the agent',
  actions: 'the action',
  conditions: 'the condition',
  goals: 'the goal',
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

/** Draft of a tab (methodology or element). */
export function draftOf(tab: Tab): Draft {
  if (tab.kind === 'methodology') return getDraft(tab.params.name ?? '', tab.params.version ?? '');
  return getDraft(tab.params.m ?? '', tab.params.v ?? '');
}

/** Key of the tab group sharing the draft. */
export function draftGroup(tab: Tab): string {
  if (tab.kind === 'methodology') return tab.params.name ? `${tab.params.name}@${tab.params.version}` : 'new';
  return `${tab.params.m}@${tab.params.v}`;
}

/** Element edited by a tab (local id, else name). */
export function resolveItem(tab: Tab): { draft: Draft; section: Section; index: number; item: SectionItem | undefined } {
  const draft = draftOf(tab);
  const section = KIND_SECTION[tab.kind];
  const index = draft.indexOf(section, tab.params.uid ?? '', tab.params.name ?? '');
  return { draft, section, index, item: index >= 0 ? draft.items(section)[index] : undefined };
}

/** If the element was found by its name, the tab picks up its local id. */
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
  if (res) notify(`${d.label} saved.`, 'ok');
  else if (d.error) notify(d.error, 'error');
}

/** Common actions of a draft's tabs. */
export function draftActions(d: Draft, extra: ToolbarAction[] = []): ToolbarAction[] {
  const busy = !!d.busy || d.loading;
  const acts: ToolbarAction[] = [];
  if (!d.readonly) {
    acts.push({
      id: 'save',
      label: d.busy === 'save' ? 'Saving…' : 'Save',
      icon: 'save',
      primary: true,
      shortcut: 'Ctrl+S',
      disabled: busy || (!d.dirty && !d.isNew),
      run: () => save(d),
    });
    if (!d.isNew) {
      acts.push({
        id: 'validate',
        label: d.busy === 'validate' ? 'Validating…' : 'Validate',
        icon: 'check',
        disabled: busy,
        run: async () => {
          await d.validate();
          if (d.error) notify(d.error, 'error');
          else notify(d.allIssues.length ? `${d.allIssues.length} issue(s) — see the "Issues" console.` : 'No issues detected.', d.allIssues.length ? 'info' : 'ok');
        },
      });
      acts.push({
        id: 'publish',
        label: d.busy === 'publish' ? 'Publishing…' : 'Publish',
        icon: 'upload',
        disabled: !d.canPublish,
        title: d.canPublish ? 'Freeze this version' : 'Save and validate the draft (with no issues) to be able to publish it',
        run: async () => {
          if (await d.publish()) notify(`${d.label} published.`, 'ok');
          else if (d.error) notify(d.error, 'error');
        },
      });
    }
  }
  if (!d.isNew) {
    acts.push({ id: 'export', label: 'Export', icon: 'download', title: 'Export to YAML', disabled: busy, run: () => d.exportYaml() });
    acts.push({
      id: 'version',
      label: 'New version',
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
      label: 'Reload',
      icon: 'refresh',
      disabled: busy,
      title: 'Reload from the registry (discards changes)',
      run: () => {
        if (d.dirty && !confirm('Discard unsaved changes and reload?')) return;
        void d.reload();
      },
    });
  }
  acts.push(...extra);
  if (!d.isNew && d.status !== 'archived') {
    acts.push({
      id: 'delete',
      label: d.status === 'draft' ? 'Delete version' : 'Archive',
      icon: 'trash',
      danger: true,
      disabled: busy,
      run: async () => {
        const r = await d.remove();
        if (r === 'deleted') {
          closeWhere((t) => ['methodology', 'agent', 'action', 'condition', 'goal'].includes(t.kind) && draftGroup(t) === d.key);
          notify(`${d.label} deleted.`, 'ok');
        } else if (r === 'archived') notify(`${d.label} archived.`, 'ok');
        else if (d.error) notify(d.error, 'error');
      },
    });
  }
  return acts;
}

// --- issues ------------------------------------------------------------------------------

/** Opens the tab concerned by an issue path and highlights the field. */
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

/** "Delete <element>" action of element tabs. */
export function removeItemAction(d: Draft, section: Section, tab: Tab, index: () => number): ToolbarAction[] {
  if (d.readonly) return [];
  return [
    {
      id: 'removeItem',
      label: `Delete ${SECTION_SINGULAR[section]}`,
      icon: 'trash',
      danger: true,
      disabled: index() < 0,
      run: () => {
        const i = index();
        const item = d.items(section)[i];
        if (!item || !confirm(`Delete ${SECTION_SINGULAR[section]} "${item.name || 'unnamed'}" from the draft?`)) return;
        d.form[section].splice(i, 1);
        closeWhere((t) => t.id === tab.id);
      },
    },
  ];
}
