// Tabs of shared domains: opening, toolbar actions.
import type { Tab, TabSpec, ToolbarAction } from '../../shell/types';
import { openTab, closeWhere } from '../../shell/tabs.svelte';
import { notify, requestReveal } from '../../shell/workbench.svelte';
import { getDomainDraft, type DomainDraft } from '../../stores/domains.svelte';

export function domainSpec(name: string, version: string): TabSpec {
  return { kind: 'domain', params: { name, version } };
}

export function openDomain(name: string, version: string, pin = false): Tab {
  return openTab(domainSpec(name, version), { pin });
}

/** Opens the domain and highlights the field at `path` (e.g. "nodeTypes[2]"). */
export function revealDomainPath(name: string, version: string, path: string): void {
  const tab = openDomain(name, version);
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
    acts.push({
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
      run: () => {
        if (d.dirty && !confirm('Discard unsaved changes and reload?')) return;
        void d.reload();
      },
    });
    if (d.status !== 'archived') {
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
