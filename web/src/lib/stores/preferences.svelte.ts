// The personal preferences of the signed-in user: theme, voice input, dashboard defaults. They are not graph data:
// the user is declared in the graph, but what they like is kept by the preferences service (ADR 0038) and saved as
// they change it, with no change to go through. The interface follows at once (and remembers the last values in the
// browser, so it starts the way the user left it); the service is the reference, read at start.
import { preferencesApi, errorMessage, type Struct } from '../api';
import { layout, type Theme } from '../shell/layout.svelte';
import { voiceSettings, type VoiceLanguage, type VoiceModel } from '../voice/settings.svelte';
import { me } from './session.svelte';

export interface Prefs {
  theme: Theme;
  voiceEnabled: boolean;
  voiceModel: VoiceModel;
  voiceLanguage: VoiceLanguage;
  /** default period of the token usage dashboard: 24h | 7d | 30d | all */
  usagePeriod: string;
  /** default scope of the token usage dashboard (administrators): mine | platform */
  usageScope: 'mine' | 'platform';
}

export const DEFAULT_PREFS: Prefs = {
  theme: 'auto',
  voiceEnabled: true,
  voiceModel: 'base',
  voiceLanguage: 'auto',
  usagePeriod: '7d',
  usageScope: 'mine',
};

export const prefs = $state({
  values: { ...DEFAULT_PREFS } as Prefs,
  error: '',
  loaded: false,
});

/** Fills the properties that are missing or of a wrong kind with the defaults. */
function normalize(raw: Record<string, unknown> | undefined): Prefs {
  const p = { ...DEFAULT_PREFS };
  if (!raw) return p;
  if (raw.theme === 'auto' || raw.theme === 'light' || raw.theme === 'dark') p.theme = raw.theme;
  if (typeof raw.voiceEnabled === 'boolean') p.voiceEnabled = raw.voiceEnabled;
  if (raw.voiceModel === 'tiny' || raw.voiceModel === 'base') p.voiceModel = raw.voiceModel;
  if (raw.voiceLanguage === 'auto' || raw.voiceLanguage === 'fr' || raw.voiceLanguage === 'en') p.voiceLanguage = raw.voiceLanguage;
  if (typeof raw.usagePeriod === 'string' && raw.usagePeriod) p.usagePeriod = raw.usagePeriod;
  if (raw.usageScope === 'mine' || raw.usageScope === 'platform') p.usageScope = raw.usageScope;
  return p;
}

/** Makes the interface follow a set of preferences. */
function applyRuntime(p: Prefs): void {
  layout.theme = p.theme;
  voiceSettings.enabled = p.voiceEnabled;
  voiceSettings.model = p.voiceModel;
  voiceSettings.language = p.voiceLanguage;
}

/** Reads the preferences of the user from the service. */
export async function loadPrefs(): Promise<void> {
  if (!me()) {
    prefs.loaded = true;
    return;
  }
  try {
    const { values } = await preferencesApi.get();
    const stored = values as Record<string, unknown> | undefined;
    // nothing stored yet: what the interface already shows (its last values in this browser) is kept, not reset
    if (stored && Object.keys(stored).length) {
      prefs.values = normalize(stored);
      applyRuntime(prefs.values);
    } else {
      prefs.values = normalize({ theme: layout.theme, voiceEnabled: voiceSettings.enabled, voiceModel: voiceSettings.model, voiceLanguage: voiceSettings.language });
    }
    prefs.error = '';
  } catch (e) {
    prefs.error = errorMessage(e);
  } finally {
    prefs.loaded = true;
  }
}

let chain: Promise<unknown> = Promise.resolve(); // writes reach the service in the order they were made

/** Changes some preferences: the interface follows at once and the service keeps them. */
export function editPrefs(patch: Partial<Prefs>): Promise<void> {
  prefs.values = { ...prefs.values, ...patch };
  applyRuntime(prefs.values);
  if (!me()) return Promise.resolve(); // anonymous: nothing to keep
  const run = () =>
    preferencesApi.set(patch as Struct).then(
      () => {
        prefs.error = '';
      },
      (e) => {
        prefs.error = errorMessage(e);
      },
    );
  chain = chain.then(run);
  return chain as Promise<void>;
}
