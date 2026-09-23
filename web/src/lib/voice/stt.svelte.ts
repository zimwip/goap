// Main-thread client of the Whisper worker: model loading state and transcription.
import type { WorkerRequest, WorkerResponse } from './stt.worker';
import { voiceSettings, type VoiceModel } from './settings.svelte';

export const stt = $state({
  /** model currently loaded (or being loaded) */
  model: '' as VoiceModel | '',
  status: 'idle' as 'idle' | 'loading' | 'ready' | 'error',
  /** download progress, 0-100 */
  progress: 0,
  device: '',
  error: '',
});

const MODEL_IDS: Record<VoiceModel, string> = {
  tiny: 'Xenova/whisper-tiny',
  base: 'Xenova/whisper-base',
};

// Base URL of a self-hosted model mirror; default is the public Hugging Face hub.
const HOST = (import.meta.env.VITE_VOICE_MODEL_HOST as string | undefined) || undefined;

let worker: Worker | undefined;
let seq = 0;
const waiting = new Map<number, { resolve: (m: WorkerResponse) => void; reject: (e: Error) => void }>();

function getWorker(): Worker {
  if (worker) return worker;
  worker = new Worker(new URL('./stt.worker.ts', import.meta.url), { type: 'module' });
  worker.onmessage = (ev: MessageEvent<WorkerResponse>) => {
    const m = ev.data;
    if (m.type === 'progress') {
      stt.progress = Math.round(m.progress);
      return;
    }
    const w = waiting.get(m.id);
    if (!w) return;
    waiting.delete(m.id);
    if (m.type === 'error') w.reject(new Error(m.message));
    else w.resolve(m);
  };
  worker.onerror = (ev) => {
    const err = new Error(ev.message || 'voice worker crashed');
    for (const w of waiting.values()) w.reject(err);
    waiting.clear();
    worker?.terminate();
    worker = undefined;
    stt.status = 'error';
    stt.error = err.message;
  };
  return worker;
}

function call(req: Omit<WorkerRequest, 'id' | 'model' | 'host'>): Promise<WorkerResponse> {
  const id = ++seq;
  const model = MODEL_IDS[voiceSettings.model];
  return new Promise((resolve, reject) => {
    waiting.set(id, { resolve, reject });
    getWorker().postMessage({ id, model, host: HOST, ...req } satisfies WorkerRequest, req.audio ? [req.audio.buffer] : []);
  });
}

/** Downloads (first time) and warms up the selected model. Safe to call repeatedly. */
export async function loadModel(): Promise<void> {
  const wanted = voiceSettings.model;
  if (stt.model === wanted && (stt.status === 'ready' || stt.status === 'loading')) return;
  stt.model = wanted;
  stt.status = 'loading';
  stt.progress = 0;
  stt.error = '';
  try {
    const r = await call({ type: 'load' });
    if (r.type === 'ready') stt.device = r.device;
    stt.status = 'ready';
  } catch (e) {
    stt.status = 'error';
    stt.error = e instanceof Error ? e.message : String(e);
    throw e;
  }
}

export async function transcribe(audio: Float32Array): Promise<string> {
  await loadModel();
  const r = await call({ type: 'transcribe', audio, language: voiceSettings.language });
  return r.type === 'result' ? r.text : '';
}
