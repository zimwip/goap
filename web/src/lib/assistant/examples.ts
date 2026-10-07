// Example requests for the empty state of the assistant: the goal examples of the methodologies applicable to the
// active project, a few of each in turn.
import { latestPublished, loadMethodology, published, refreshMethodologies, methodologies } from '../stores/catalog.svelte';
import { applicable } from '../stores/project.svelte';

/** Reads what the examples need (once per methodology). */
export async function loadExamples(): Promise<void> {
  if (!methodologies.loaded) await refreshMethodologies();
  await Promise.all(applicableNames().map(loadMethodology));
}

function applicableNames(): string[] {
  return latestPublished()
    .map((m) => m.name ?? '')
    .filter((n) => n && (!applicable.names || applicable.names.includes(n)));
}

/** At most `max` examples, taken from each methodology in turn. */
export function examplePrompts(max = 4): string[] {
  const lists = applicableNames().map((n) => (published.get(n)?.goals ?? []).flatMap((g) => g.examples ?? []).filter(Boolean));
  const out: string[] = [];
  for (let i = 0; out.length < max && lists.some((l) => i < l.length); i++) {
    for (const l of lists) if (i < l.length && out.length < max && !out.includes(l[i])) out.push(l[i]);
  }
  return out;
}
