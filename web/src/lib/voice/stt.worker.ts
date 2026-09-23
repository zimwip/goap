// Whisper speech-to-text in a Web Worker (transformers.js). Runs fully in the browser.
import { env, pipeline } from '@huggingface/transformers';

export interface WorkerRequest {
  id: number;
  type: 'load' | 'transcribe';
  model: string;
  audio?: Float32Array;
  language?: string;
  /** optional self-hosted model host (air-gapped deployments) */
  host?: string;
}

export type WorkerResponse =
  | { id: number; type: 'progress'; progress: number }
  | { id: number; type: 'ready'; device: string }
  | { id: number; type: 'result'; text: string }
  | { id: number; type: 'error'; message: string };

env.allowLocalModels = false;
env.useBrowserCache = true;

type Recognizer = (audio: Float32Array, opts: Record<string, unknown>) => Promise<{ text: string } | { text: string }[]>;

const loaded = new Map<string, Promise<{ run: Recognizer; device: string }>>();

function load(model: string, onProgress: (p: number) => void) {
  let p = loaded.get(model);
  if (!p) {
    p = (async () => {
      const device = 'gpu' in navigator ? 'webgpu' : 'wasm';
      const files = new Map<string, number>();
      const progress_callback = (e: { status?: string; file?: string; progress?: number }) => {
        if (e.status === 'progress' && e.file) {
          files.set(e.file, e.progress ?? 0);
          const all = [...files.values()];
          onProgress(all.reduce((a, b) => a + b, 0) / all.length);
        }
      };
      const build = (d: 'webgpu' | 'wasm') =>
        pipeline('automatic-speech-recognition', model, {
          device: d,
          dtype: d === 'webgpu' ? { encoder_model: 'fp32', decoder_model_merged: 'q4' } : 'q8',
          progress_callback,
        });
      let rec;
      let used = device;
      try {
        rec = await build(device as 'webgpu' | 'wasm');
      } catch (e) {
        if (device === 'wasm') throw e;
        used = 'wasm';
        rec = await build('wasm');
      }
      return { run: rec as unknown as Recognizer, device: used };
    })();
    p.catch(() => loaded.delete(model));
    loaded.set(model, p);
  }
  return p;
}

self.onmessage = async (ev: MessageEvent<WorkerRequest>) => {
  const { id, type, model, audio, language, host } = ev.data;
  const post = (m: WorkerResponse) => self.postMessage(m);
  if (host) env.remoteHost = host;
  try {
    const { run, device } = await load(model, (progress) => post({ id, type: 'progress', progress }));
    if (type === 'load') {
      post({ id, type: 'ready', device });
      return;
    }
    const opts: Record<string, unknown> = { task: 'transcribe', chunk_length_s: 30, stride_length_s: 5 };
    if (language && language !== 'auto') opts.language = language === 'fr' ? 'french' : 'english';
    const out = await run(audio ?? new Float32Array(), opts);
    const text = (Array.isArray(out) ? out.map((o) => o.text).join(' ') : out.text).trim();
    post({ id, type: 'result', text });
  } catch (e) {
    post({ id, type: 'error', message: e instanceof Error ? e.message : String(e) });
  }
};
