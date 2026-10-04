// Tabs of domains: opening, toolbar actions.
import type { Tab, TabSpec, ToolbarAction } from '../../shell/types';
import { openTab, closeWhere } from '../../shell/tabs.svelte';
import { notify, requestReveal } from '../../shell/workbench.svelte';
import { getDomainDraft, type DomainDraft } from '../../stores/domains.svelte';
import { confirmDialog } from '../../shell/confirmState.svelte';

export function domainSpec(name: string, version: string): TabSpec {
  return { kind: 'domain', params: { name, version } };
}

export function openDomain(name: string, version: string, pin = false): Tab {
  return openTab(domainSpec(name, version), { pin });
}

/**
 * Opens the editor of what `path` designates ("nodeTypes[2]", "lifecycles[0].states[1]"…) and highlights it:
 * a node type, link type or lifecycle has its own tab, anything else is a field of the domain tab.
 */
export function revealDomainPath(name: string, version: string, path: string, pin = false): void {
  const m = /^(nodeTypes|linkTypes|lifecycles|enums)\[(\d+)\]/.exec(path);
  const d = m ? getDomainDraft(name, version) : undefined;
  const i = m ? Number(m[2]) : -1;
  const tab =
    m && d && i < d.form[m[1] as 'nodeTypes' | 'linkTypes' | 'lifecycles' | 'enums'].length
      ? m[1] === 'nodeTypes'
        ? openNodeType(d, i, pin)
        : m[1] === 'linkTypes'
          ? openLinkType(d, i, pin)
          : m[1] === 'enums'
            ? openEnum(d, i, pin)
            : openLifecycle(d, i, pin)
      : openDomain(name, version, pin);
  requestReveal(tab.id, path);
}

export function domainDraftOf(tab: Tab): DomainDraft {
  return getDomainDraft(tab.params.name ?? '', tab.params.version ?? '');
}

export function domainGroup(tab: Tab): string {
  return tab.params.name ? `${tab.params.name}@${tab.params.version}` : 'new';
}

async function save(d: DomainDraft): Promise<void> {
  const res = await d.save();
  if (res) notify(`${d.label} saved.`, 'ok');
  else if (d.error) notify(d.error, 'error');
}

/** Toolbar actions of a domain tab. */
export function domainActions(d: DomainDraft): ToolbarAction[] {
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
          else notify(d.allIssues.length ? `${d.allIssues.length} issue(s) in the domain.` : 'No issues detected.', d.allIssues.length ? 'info' : 'ok');
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
    if (!d.builtin) acts.push({
      id: 'version',
      label: 'New version',
      icon: 'copy',
      disabled: busy,
      run: async () => {
        const v = await d.newVersion();
        if (v) openDomain(d.name, v, true);
        else if (d.error) notify(d.error, 'error');
      },
    });
    acts.push({
      id: 'reload',
      label: 'Reload',
      icon: 'refresh',
      disabled: busy,
      title: 'Reload from the registry (discards changes)',
      run: async () => {
        if (d.dirty && !(await confirmDialog('Discard unsaved changes and reload?'))) return;
        void d.reload();
      },
    });
    if (d.status !== 'archived' && !d.builtin) {
      acts.push({
        id: 'delete',
        label: d.status === 'draft' ? 'Delete version' : 'Archive',
        icon: 'trash',
        danger: true,
        disabled: busy,
        run: async () => {
          const r = await d.remove();
          if (r === 'deleted') {
            closeWhere((t) => t.kind === 'domain' && domainGroup(t) === d.key);
            notify(`${d.label} deleted.`, 'ok');
          } else if (r === 'archived') notify(`${d.label} archived.`, 'ok');
          else if (d.error) notify(d.error, 'error');
        },
      });
    }
  }
  return acts;
}

// --- algorithms (ADR 0018) -------------------------------------------------------------

export function algorithmSpec(name: string, version: string, uid: string, alg = ''): TabSpec {
  return { kind: 'algorithm', params: { name, version, uid, alg } };
}

export function instanceSpec(name: string, version: string, uid: string, inst = ''): TabSpec {
  return { kind: 'instance', params: { name, version, uid, inst } };
}

export function openAlgorithm(d: DomainDraft, index: number, pin = true): Tab {
  const a = d.form.algorithms[index];
  return openTab(algorithmSpec(d.name, d.version, a.uid, a.name), { pin });
}

export function openInstance(d: DomainDraft, index: number, pin = true): Tab {
  const i = d.form.instances[index];
  return openTab(instanceSpec(d.name, d.version, i.uid, i.name), { pin });
}

/** Index of the algorithm / instance a tab shows: by local id, else by name (after a reload). */
export function algorithmIndex(d: DomainDraft, tab: Tab): number {
  const i = d.form.algorithms.findIndex((a) => a.uid === tab.params.uid);
  return i >= 0 ? i : d.form.algorithms.findIndex((a) => a.name === tab.params.alg);
}

export function instanceIndex(d: DomainDraft, tab: Tab): number {
  const i = d.form.instances.findIndex((x) => x.uid === tab.params.uid);
  return i >= 0 ? i : d.form.instances.findIndex((x) => x.name === tab.params.inst);
}

/** Toolbar actions of an algorithm / instance tab: those of the domain (they share its draft) and a delete. */
export function algorithmToolbar(d: DomainDraft, remove: { label: string; run: () => void }): ToolbarAction[] {
  const acts = domainActions(d).filter((a) => ['save', 'validate', 'publish'].includes(a.id));
  if (!d.readonly) acts.push({ id: 'remove', label: remove.label, icon: 'trash', danger: true, run: remove.run });
  return acts;
}

// --- node types, link types, lifecycles: one tab each --------------------------------------

export function nodeTypeSpec(name: string, version: string, uid: string, nt = ''): TabSpec {
  return { kind: 'nodetype', params: { name, version, uid, nt } };
}

export function linkTypeSpec(name: string, version: string, uid: string, lt = ''): TabSpec {
  return { kind: 'linktype', params: { name, version, uid, lt } };
}

export function lifecycleSpec(name: string, version: string, uid: string, lc = ''): TabSpec {
  return { kind: 'lifecycle', params: { name, version, uid, lc } };
}

export function enumSpec(name: string, version: string, uid: string, en = ''): TabSpec {
  return { kind: 'enum', params: { name, version, uid, en } };
}

export function openEnum(d: DomainDraft, index: number, pin = true): Tab {
  const e = d.form.enums[index];
  return openTab(enumSpec(d.name, d.version, e.uid, e.name), { pin });
}

export function openNodeType(d: DomainDraft, index: number, pin = true): Tab {
  const n = d.form.nodeTypes[index];
  return openTab(nodeTypeSpec(d.name, d.version, n.uid, n.name), { pin });
}

export function openLinkType(d: DomainDraft, index: number, pin = true): Tab {
  const l = d.form.linkTypes[index];
  return openTab(linkTypeSpec(d.name, d.version, l.uid, l.name), { pin });
}

export function openLifecycle(d: DomainDraft, index: number, pin = true): Tab {
  const l = d.form.lifecycles[index];
  return openTab(lifecycleSpec(d.name, d.version, l.uid, l.name), { pin });
}

/** Index of the item a tab shows: by local id, else by name (after a reload). */
function indexIn<T extends { uid: string; name: string }>(list: T[], tab: Tab, nameParam: string): number {
  const i = list.findIndex((x) => x.uid === tab.params.uid);
  return i >= 0 ? i : list.findIndex((x) => x.name === tab.params[nameParam]);
}

export const nodeTypeIndex = (d: DomainDraft, tab: Tab) => indexIn(d.form.nodeTypes, tab, 'nt');
export const linkTypeIndex = (d: DomainDraft, tab: Tab) => indexIn(d.form.linkTypes, tab, 'lt');
export const lifecycleIndex = (d: DomainDraft, tab: Tab) => indexIn(d.form.lifecycles, tab, 'lc');
export const enumIndex = (d: DomainDraft, tab: Tab) => indexIn(d.form.enums, tab, 'en');
