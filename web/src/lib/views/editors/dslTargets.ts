// What each editor tells the "DSL Help" panel about the script it holds (`EditorView.help`).
import type { Tab } from '../../shell/types';
import { peekDraft } from '../../stores/drafts.svelte';
import { peekDomainDraft } from '../../stores/domains.svelte';
import { algorithmUsage } from '../../dsl';
import { plugsOf, type AlgorithmForm, type InstanceForm } from '../../algorithmForm';
import type { DslTarget, HelpLink, HelpParam, HelpUsage } from '../../dslHelp';
import { draftGroup, itemSpec } from './methodologyTabs';
import { algorithmIndex, algorithmSpec, domainGroup, instanceIndex } from './domainTabs';

function paramsOf(a: AlgorithmForm, values?: Record<string, unknown>): HelpParam[] {
  return a.params
    .filter((p) => p.name.trim())
    .map((p) => ({
      name: p.name.trim(),
      type: p.type,
      description: p.description,
      required: p.required,
      defaultValue: p.defaultValue,
      values: p.values,
      // a secret is a reference, never shown back
      value: values && p.type !== 'secret' && p.name in values ? values[p.name] : undefined,
    }));
}

/** An action tab: the script of an action of kind script, or why the action holds none. */
export function actionHelp(t: Tab): DslTarget | undefined {
  const d = peekDraft(draftGroup(t));
  if (!d) return undefined;
  const i = d.indexOf('actions', t.params.uid ?? '', t.params.name ?? '');
  const a = i >= 0 ? d.form.actions[i] : undefined;
  if (!a) return undefined;
  const title = a.name || '(unnamed)';
  const subtitle = `Action · ${d.label}`;
  if (a.kind !== 'script') {
    return { title, subtitle, note: `This action is of kind ${a.kind || 'unset'}: it runs no script. Set its kind to script to write one.` };
  }
  return { usage: 'action', title, subtitle };
}

/** An algorithm tab: its usage and declared parameters. */
export function algorithmHelp(t: Tab): DslTarget | undefined {
  const d = peekDomainDraft(domainGroup(t));
  if (!d) return undefined;
  const i = algorithmIndex(d, t);
  const a = i >= 0 ? d.form.algorithms[i] : undefined;
  if (!a) return undefined;
  const usage = algorithmUsage(a.type)?.usage as HelpUsage | undefined;
  return { usage, title: a.name || '(unnamed)', subtitle: `Algorithm · ${d.label}`, params: paramsOf(a) };
}

/** An instance tab: the algorithm it sets the values of, and where it is plugged. */
export function instanceHelp(t: Tab): DslTarget | undefined {
  const d = peekDomainDraft(domainGroup(t));
  if (!d) return undefined;
  const i = instanceIndex(d, t);
  const inst: InstanceForm | undefined = i >= 0 ? d.form.instances[i] : undefined;
  if (!inst) return undefined;
  const title = inst.name || '(unnamed)';
  const subtitle = `Algorithm instance · ${d.label}`;
  const a = d.form.algorithms.find((x) => x.name === inst.algorithm && x.name);
  if (!a) return { title, subtitle, note: 'Choose the algorithm this instance sets the values of.' };
  const plugs = plugsOf(inst.name, d.form.nodeTypes, d.form.linkTypes, d.form.lifecycles);
  return {
    usage: algorithmUsage(a.type)?.usage as HelpUsage | undefined,
    title,
    subtitle: `${subtitle} · of ${a.name}`,
    params: paramsOf(a, inst.values),
    plugs,
  };
}

/** An adapter tab: its code is the adapter usage. */
export function adapterHelp(t: Tab): DslTarget {
  return { usage: 'adapter', title: t.params.name || 'New adapter', subtitle: 'Adapter definition' };
}

const scriptLink = (label: string, detail: string, spec: HelpLink['spec']): HelpLink => ({ label: label || '(unnamed)', detail, spec });

/** A methodology tab: the script actions of the draft. */
export function methodologyHelp(t: Tab): DslTarget | undefined {
  const d = peekDraft(draftGroup(t));
  if (!d || d.isNew) return undefined;
  const scripts = d.form.actions.filter((a) => a.kind === 'script');
  return {
    title: d.label,
    subtitle: 'Methodology',
    note: scripts.length ? undefined : 'No script action in this methodology. Add an action of kind script to write one.',
    links: scripts.length
      ? { heading: 'Script actions', items: scripts.map((a) => scriptLink(a.name, 'JavaScript', itemSpec(d, 'actions', a))) }
      : undefined,
  };
}

/** A domain tab: its algorithms, by usage. */
export function domainHelp(t: Tab): DslTarget | undefined {
  const d = peekDomainDraft(domainGroup(t));
  if (!d || d.isNew) return undefined;
  const algs = d.form.algorithms;
  return {
    title: d.label,
    subtitle: 'Domain',
    note: algs.length ? undefined : 'No algorithm in this domain yet. Add one from the domain explorer.',
    links: algs.length
      ? {
          heading: 'Algorithms',
          items: algs.map((a) => scriptLink(a.name, algorithmUsage(a.type)?.title ?? a.type, algorithmSpec(d.name, d.version, a.uid, a.name))),
        }
      : undefined,
  };
}
