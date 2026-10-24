// Voice input preferences (localStorage). Audio is always transcribed locally.
import { loadRaw, save } from '../shell/storage';

/** `auto`: the device decides (plan.ts) */
export type VoiceModel = 'auto' | 'tiny' | 'base' | 'small';
export type VoiceLanguage = 'auto' | 'fr' | 'en';

interface VoiceSettings {
  enabled: boolean;
  model: VoiceModel;
  language: VoiceLanguage;
}

const KEY = 'goap.ide.voice';

export function isVoiceModel(v: unknown): v is VoiceModel {
  return v === 'auto' || v === 'tiny' || v === 'base' || v === 'small';
}

function restore(): VoiceSettings {
  const raw = loadRaw(KEY) as Partial<VoiceSettings> | undefined;
  return {
    enabled: raw?.enabled !== false,
    model: isVoiceModel(raw?.model) ? raw.model : 'auto',
    language: raw?.language === 'fr' || raw?.language === 'en' ? raw.language : 'auto',
  };
}

export const voiceSettings: VoiceSettings = $state(restore());

$effect.root(() => {
  $effect(() => {
    save(KEY, $state.snapshot(voiceSettings));
  });
});
