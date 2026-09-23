<script lang="ts">
  // Push-to-talk: hold the button (or Space / Enter) to record, release to transcribe locally.
  import Icon from '../shell/Icon.svelte';
  import { Recorder, voiceSupported } from './recorder';
  import { stt, transcribe, loadModel } from './stt.svelte';
  import { voiceSettings } from './settings.svelte';

  let { ontranscript }: { ontranscript: (text: string) => void } = $props();

  let phase = $state<'idle' | 'recording' | 'transcribing'>('idle');
  let error = $state('');
  let recorder: Recorder | undefined;
  let held = false;

  const supported = voiceSupported();

  const title = $derived.by(() => {
    if (error) return error;
    if (phase === 'recording') return 'Release to transcribe';
    if (phase === 'transcribing') return 'Transcribing…';
    if (stt.status === 'loading') return `Downloading the speech model… ${stt.progress}%`;
    return 'Hold to speak (audio is processed locally, never uploaded)';
  });

  async function begin(e: Event) {
    e.preventDefault();
    if (phase !== 'idle') return;
    error = '';
    held = true;
    recorder = new Recorder();
    try {
      await recorder.start();
      if (!held) {
        recorder.cancel();
        recorder = undefined;
        return;
      }
      phase = 'recording';
      void loadModel().catch(() => undefined);
    } catch (err) {
      recorder = undefined;
      error = err instanceof DOMException && err.name === 'NotAllowedError' ? 'Microphone access denied' : 'Microphone unavailable';
    }
  }

  async function end() {
    held = false;
    if (phase !== 'recording' || !recorder) return;
    const rec = recorder;
    recorder = undefined;
    phase = 'transcribing';
    try {
      const samples = await rec.stop();
      if (samples.length) {
        const text = await transcribe(samples);
        if (text) ontranscript(text);
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      phase = 'idle';
    }
  }

  function keydown(e: KeyboardEvent) {
    if ((e.key === ' ' || e.key === 'Enter') && !e.repeat) void begin(e);
  }
  function keyup(e: KeyboardEvent) {
    if (e.key === ' ' || e.key === 'Enter') void end();
  }
</script>

{#if supported && voiceSettings.enabled}
  <button
    type="button"
    class="voice"
    class:rec={phase === 'recording'}
    {title}
    aria-label="Hold to speak"
    aria-pressed={phase === 'recording'}
    disabled={phase === 'transcribing'}
    onpointerdown={begin}
    onpointerup={end}
    onpointerleave={end}
    onpointercancel={end}
    onkeydown={keydown}
    onkeyup={keyup}
  >
    <Icon name="mic" size={14} />
    {#if phase === 'recording'}Recording…{:else if phase === 'transcribing'}…{:else if stt.status === 'loading'}{stt.progress}%{/if}
  </button>
  {#if error}<span class="verr" role="alert">{error}</span>{/if}
{/if}

<style>
  .voice {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.3rem;
    touch-action: none;
    user-select: none;
  }
  .voice.rec {
    background: var(--danger);
    border-color: var(--danger);
    color: #fff;
  }
  .verr {
    color: var(--danger);
    font-size: 0.85em;
  }
</style>
