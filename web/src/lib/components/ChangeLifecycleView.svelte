<script lang="ts">
  // The lifecycle a change follows (ADR 0058), as drawn in the lifecycle definition of its domain, with the state the
  // change is in. Shown when the change has one.
  import { registry, errorMessage, type Lifecycle } from '../api';
  import { lifecycleToForm } from '../methodologyForm';
  import LifecyclePreview from './LifecyclePreview.svelte';

  let { lifecycle, current = '', namespace = '' }: { lifecycle: string; current?: string; namespace?: string } = $props();

  let found = $state<Lifecycle>();
  let domainName = $state('');
  let failure = $state('');
  let loading = $state(false);

  const lc = $derived(found ? lifecycleToForm(found) : undefined);
  const transitions = $derived((found?.transitions ?? []).filter((t) => t.from === current));

  /** the lifecycle of the domain of the namespace, else of any other domain (a built-in one for instance) */
  async function find(name: string, ns: string, signal: AbortSignal): Promise<{ lc: Lifecycle; domain: string } | undefined> {
    const names = (await registry.listDomains(false, signal)).domains?.map((d) => d.name ?? '') ?? [];
    for (const dn of [ns, ...names.filter((n) => n !== ns)].filter(Boolean)) {
      if (!names.includes(dn)) continue;
      const d = (await registry.getDomain(dn, '', signal)).domain;
      const lc = d?.lifecycles?.find((l) => l.name === name);
      if (lc) return { lc, domain: dn };
    }
    return undefined;
  }

  $effect(() => {
    const name = lifecycle;
    const ns = namespace;
    found = undefined;
    failure = '';
    if (!name) return;
    const ctl = new AbortController();
    loading = true;
    find(name, ns, ctl.signal)
      .then((r) => {
        if (r) {
          found = r.lc;
          domainName = r.domain;
        } else failure = `No domain defines the lifecycle “${name}”.`;
      })
      .catch((e) => {
        if (!ctl.signal.aborted) failure = errorMessage(e);
      })
      .finally(() => {
        if (!ctl.signal.aborted) loading = false;
      });
    return () => ctl.abort();
  });
</script>

<section class="card change-lifecycle">
  <div class="head">
    <strong>Lifecycle</strong>
    <code>{lifecycle}</code>
    {#if domainName}<span class="hint">{domainName}</span>{/if}
    <span class="grow"></span>
    {#if current}<span class="hint">state <code>{current}</code>{#if transitions.length} → {transitions.map((t) => t.name).join(', ')}{/if}</span>{/if}
  </div>
  {#if loading}
    <p class="hint">Loading…</p>
  {:else if failure}
    <p class="hint">{failure}</p>
  {:else if lc}
    <LifecyclePreview {lc} {current} />
  {/if}
</section>

<style>
  .head {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
  }
  .grow {
    flex: 1;
  }
</style>
