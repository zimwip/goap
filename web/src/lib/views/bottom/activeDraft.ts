// Methodology draft of the active tab (without creating it).
import { activeTab } from '../../shell/tabs.svelte';
import { peekDraft, type Draft } from '../../stores/drafts.svelte';
import { draftGroup, isDraftTab } from '../editors/methodologyTabs';

export function activeDraft(): Draft | undefined {
  const t = activeTab();
  return t && isDraftTab(t) ? peekDraft(draftGroup(t)) : undefined;
}
