// Voice input preferences (localStorage). Audio is always transcribed locally.
import { loadRaw, save } from '../shell/storage';

export type VoiceModel = 'tiny' | 'base';
export type VoiceLanguage = 'auto' | 'fr' | 'en';

interface VoiceSettings {
  enabled: boolean;
  model: VoiceModel;
  language: VoiceLanguage;
}

const KEY = 'goap.ide.voice';

function restore(): VoiceSettings {
  const raw = loadRaw(KEY) as Partial<VoiceSettings> | undefined;
  return {
    enabled: raw?.enabled !== false,
    model: raw?.model === 'tiny' ? 'tiny' : 'base',
    language: raw?.language === 'fr' || raw?.language === 'en' ? raw.language : 'auto',
  };
}

export const voiceSettings: VoiceSettings = $state(restore());

$effect.root(() => {
  $effect(() => {
    save(KEY, $state.snapshot(voiceSettings));
  });
});
