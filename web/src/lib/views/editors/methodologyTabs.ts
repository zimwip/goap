// Tabs tied to a methodology draft: opening elements, common toolbar
// actions, resolving issues.
import type { IconName } from '../../shell/Icon.svelte';
import type { Tab, TabSpec, ToolbarAction } from '../../shell/types';
import { openTab, closeWhere, replaceTab } from '../../shell/tabs.svelte';
import { notify, requestReveal } from '../../shell/workbench.svelte';
import { editorView } from '../../shell/registry';
import { tabId } from '../../shell/tabs.svelte';
import { getDraft, type Draft } from '../../stores/drafts.svelte';
import { confirmDialog } from '../../shell/confirmState.svelte';
import {
  emptyAgent,
  emptyAction,
  emptyCondition,
  emptyGoal,
  emptyProcess,
  emptyMethod,
  emptyStep,
  type Section,
  type StepForm,
  type SectionItem,
} from '../../methodologyForm';
import { typeName, type TypeCatalog } from '../../stores/types.svelte';

export const SECTION_KIND: Record<Section, string> = {
  agents: 'agent',
  actions: 'action',
  conditions: 'condition',
  goals: 'goal',
  processes: 'process',
  methods: 'method',
};

export const KIND_SECTION: Record<string, Section> = {
  agent: 'agents',
  action: 'actions',
  condition: 'conditions',
  goal: 'goals',
  process: 'processes',
  method: 'methods',
};

export const SECTION_LABEL: Record<Section, string> = {
  agents: 'Agents',
  actions: 'Actions',
  conditions: 'Conditions',
  goals: 'Goals',
  processes: 'Processes',
  methods: 'Methods',
};

export const SECTION_ICON: Record<Section, IconName> = {
  agents: 'bot',
  actions: 'zap',
  conditions: 'branch',
  goals: 'target',
  processes: 'list',
  methods: 'book',
};

/** Singular name of an element, for messages. */
export const SECTION_SINGULAR: Record<Section, string> = {
  agents: 'the agent',
  actions: 'the action',
  conditions: 'the condition',
  goals: 'the goal',
  processes: 'the process',
  methods: 'the method',
};

/** The type the methodology version is composed of its elements from (its `defines` composition link). */
export const VERSION_TYPE = 'methodology@MethodologyVersion';

/** What an editor pane lists: a collection of the draft. */
export type Collection = Section | 'roles';

/** The node types of the methodology domain, bound to the collection of the draft that holds their elements. */
export const TYPE_COLLECTION: Record<string, Collection> = {
  Agent: 'agents',
  Action: 'actions',
  Condition: 'conditions',
  Goal: 'goals',
  Process: 'processes',
  Method: 'methods',
  Role: 'roles',
};

export const COLLECTION_TYPE: Record<Collection, string> = Object.fromEntries(Object.entries(TYPE_COLLECTION).map(([t, c]) => [c, t])) as Record<Collection, string>;

/** The panes before the type catalogue is loaded. */
export const DEFAULT_COLLECTIONS: Collection[] = ['processes', 'methods', 'agents', 'actions', 'conditions', 'goals', 'roles'];

/** The panes of a methodology version: the element types the domain says it is composed of (`defines`). */
export function sectionsFor(cat: TypeCatalog): Collection[] {
  const cols = cat.sectionsOf(VERSION_TYPE).map((t) => TYPE_COLLECTION[typeName(t)]).filter((c): c is Collection => !!c);
  return cols.length ? cols : DEFAULT_COLLECTIONS;
}

export const COLLECTION_LABEL = (c: Collection): string => (c === 'roles' ? 'Roles' : SECTION_LABEL[c]);
export const COLLECTION_ICON = (c: Collection): IconName => (c === 'roles' ? 'user' : SECTION_ICON[c]);
/** "agent", "role"...: what one element of the collection is called. */
export const collectionNoun = (c: Collection): string => COLLECTION_TYPE[c].toLowerCase();

/** Does an element of the collection have steps (a composite of the domain)? */
export function hasSteps(cat: TypeCatalog, c: Collection, it: unknown): it is { steps: import('../../methodologyForm').StepForm[] } {
  return c !== 'roles' && !!it && typeof it === 'object' && 'steps' in it && cat.isComposite(`${VERSION_TYPE.split('@')[0]}@${COLLECTION_TYPE[c]}`);
}

const FACTORIES = { agents: emptyAgent, actions: emptyAction, conditions: emptyCondition, goals: emptyGoal, processes: emptyProcess, methods: emptyMethod };

/** Adds an element to the draft and opens it (a role: on the Roles pane of the methodology tab). */
export function addElement(d: Draft, c: Collection): void {
  if (c === 'roles') {
    d.form.roles.push({ name: '', description: '' });
    const t = openTab(methodologySpec(d.name, d.version), { pin: true });
    t.params.pane = 'roles';
    requestReveal(t.id, `roles[${d.form.roles.length - 1}]`);
    return;
  }
  (d.form[c] as SectionItem[]).push(FACTORIES[c]());
  openItem(d, c, d.form[c][d.form[c].length - 1], true);
}

/** Adds a step to a process or a method (a part of it, by composition) and opens it in its own editor. */
export function addStep(d: Draft, c: Section, it: { steps: StepForm[] }): void {
  const step = emptyStep(`step_${it.steps.length + 1}`);
  it.steps.push(step);
  openStep(d, step, it as unknown as SectionItem, true);
}

/** Where a step sits: its process or method, the list it belongs to and its issue path in the owner. */
export interface StepLocation {
  section: 'processes' | 'methods';
  owner: SectionItem & { steps: StepForm[] };
  step: StepForm;
  siblings: StepForm[];
  /** issue path inside the owner, "steps[1].steps[0]" */
  at: string;
  /** server path: "<owner>/<step>/<sub-step>" */
  path: string;
}

/** Finds a step of the draft by its local key. */
export function findStep(d: Draft, key: string): StepLocation | undefined {
  for (const section of ['processes', 'methods'] as const) {
    for (const owner of d.form[section] as (SectionItem & { steps: StepForm[] })[]) {
      const hit = (list: StepForm[], at: string, prefix: string): StepLocation | undefined => {
        for (let i = 0; i < list.length; i++) {
          const here = `${at}[${i}]`;
          const path = `${prefix}/${list[i].name}`;
          if (list[i].key === key) return { section, owner, step: list[i], siblings: list, at: here, path };
          const sub = hit(list[i].steps, `${here}.steps`, path);
          if (sub) return sub;
        }
        return undefined;
      };
      const found = hit(owner.steps, 'steps', owner.name);
      if (found) return found;
    }
  }
  return undefined;
}

export function stepSpec(d: Draft, step: StepForm, owner: SectionItem): TabSpec {
  return { kind: 'step', params: { m: d.name, v: d.version, skey: step.key, uid: owner.uid, name: step.name } };
}

export function openStep(d: Draft, step: StepForm, owner: SectionItem, pin = false): void {
  openTab(stepSpec(d, step, owner), { pin });
}

/** Adds a step under a step (turning it into a step done by sub-steps) and opens it. */
export function addSubStep(d: Draft, owner: SectionItem, step: StepForm): void {
  step.method = 'steps';
  const sub = emptyStep(`step_${step.steps.length + 1}`);
  step.steps.push(sub);
  openStep(d, sub, owner, true);
}

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
      run: async () => {
        if (d.dirty && !(await confirmDialog('Discard unsaved changes and reload?'))) return;
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
          closeWhere((t) => ['methodology', 'agent', 'action', 'condition', 'goal', 'process', 'method', 'step'].includes(t.kind) && draftGroup(t) === d.key);
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
  const m = /^(agents|actions|conditions|goals|processes|methods)\[(\d+)\]/.exec(path);
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
  return !!tab && !!editorView(tab.kind) && ['methodology', 'agent', 'action', 'condition', 'goal', 'process', 'method', 'step'].includes(tab.kind);
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
      run: async () => {
        const i = index();
        const item = d.items(section)[i];
        if (!item || !(await confirmDialog({ message: `Delete ${SECTION_SINGULAR[section]} "${item.name || 'unnamed'}" from the draft?`, danger: true })))
          return;
        d.form[section].splice(i, 1);
        closeWhere((t) => t.id === tab.id);
      },
    },
  ];
}
