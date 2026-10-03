// The reducers of the front (ADR 0053): what each event of the platform stream says changed, written as
// invalidation signals (`signals.svelte.ts`), and the shared catalogs read again when theirs move. Views never
// refetch after a write: the event comes back, whoever made the write, and everything showing it follows.
import { untrack } from 'svelte';
import { onKind, onResync } from './events.svelte';
import { stamp, touchAll, touchSoon } from './signals.svelte';
import { keysOf } from './keys';
import { keyOf } from './signals.svelte';
import { baselines, changes, methodologies, refreshBaselines, refreshChanges, refreshMethodologies, baselinesNamespace } from '../stores/catalog.svelte';
import { refreshBranches } from '../stores/baselineTool.svelte';
import { draftKey, peekDraft } from '../stores/drafts.svelte';
import { isOwnCommand } from './commands';
import { domainDrafts, domainKey, domains, refreshDomains } from '../stores/domains.svelte';
import { loadTypes } from '../stores/types.svelte';
import { tools, refreshTools } from '../stores/tools.svelte';
import { refreshProjects } from '../stores/project.svelte';
import { modelChoices, refreshModelChoices } from '../stores/modelChoices.svelte';

/** Starts the reducers; returns the stop function. */
export function startReducers(): () => void {
  const offs = [
    onKind(['change', 'node', 'baseline', 'methodology', 'domain'], (e) => {
      for (const k of keysOf(e)) touchSoon(k);
    }),
    // a version somebody else saved, published or removed, in an open draft
    onKind('methodology', (e) => {
      if (isOwnCommand(e.commandId)) return;
      peekDraft(draftKey(e.id, e.label))?.externalChange(e.actor, e.type);
    }),
    onKind('domain', (e) => {
      if (isOwnCommand(e.commandId)) return;
      domainDrafts.get(domainKey(e.id, e.label))?.externalChange(e.actor, e.type);
    }),
    onResync(touchAll),
  ];
  // the shared catalogs read again, once loaded, when what they list moves
  const stop = $effect.root(() => {
    $effect(() => {
      void stamp(keyOf.changes);
      untrack(() => changes.loaded && void refreshChanges());
    });
    $effect(() => {
      void stamp(keyOf.baselines);
      untrack(() => {
        if (baselines.loaded && baselinesNamespace()) void refreshBaselines(baselinesNamespace());
        refreshBranches();
      });
    });
    $effect(() => {
      if (!stamp(keyOf.domains)) return;
      untrack(() => {
        if (domains.loaded) void refreshDomains();
        void loadTypes(true); // the type catalogue follows the published domains
      });
    });
    // the organisation and platform namespaces hold the projects, adapters and MCPs: what lists them follows
    $effect(() => {
      if (!stamp(keyOf.namespace('platform'))) return;
      untrack(() => {
        if (tools.loaded) void refreshTools();
        // aliases and providers are nodes of the platform namespace
        if (modelChoices.loaded) void refreshModelChoices();
      });
    });
    $effect(() => {
      if (!stamp(keyOf.namespace('organisation'))) return;
      untrack(() => void refreshProjects());
    });
    $effect(() => {
      void stamp(keyOf.methodologies);
      untrack(() => methodologies.loaded && void refreshMethodologies());
    });
  });
  return () => {
    for (const off of offs) off();
    stop();
  };
}
