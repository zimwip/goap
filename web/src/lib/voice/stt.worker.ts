// Whisper speech-to-text in a Web Worker (transformers.js). Runs fully in the browser.
import { env, pipeline } from '@huggingface/transformers';
import { onCPU, type Plan } from './plan';

export interface WorkerRequest {
  id: number;
  type: 'load' | 'transcribe';
  plan: Plan;
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

function load(plan: Plan, onProgress: (p: number) => void) {
  const key = JSON.stringify(plan);
  let p = loaded.get(key);
  if (!p) {
    p = (async () => {
      const files = new Map<string, number>();
      const progress_callback = (e: { status?: string; file?: string; progress?: number }) => {
        if (e.status === 'progress' && e.file) {
          files.set(e.file, e.progress ?? 0);
          const all = [...files.values()];
          onProgress(all.reduce((a, b) => a + b, 0) / all.length);
        }
      };
      // the dtype is a per-file map or one name, both of which transformers.js takes
      const build = (q: Plan) =>
        pipeline('automatic-speech-recognition', q.model, { device: q.device, dtype: q.dtype as never, progress_callback });
      let rec;
      let used = plan;
      try {
        rec = await build(plan);
      } catch (e) {
        if (plan.device === 'wasm') throw e;
        used = onCPU(plan);
        rec = await build(used);
      }
      return { run: rec as unknown as Recognizer, device: used.device };
    })();
    p.catch(() => loaded.delete(key));
    loaded.set(key, p);
  }
  return p;
}

self.onmessage = async (ev: MessageEvent<WorkerRequest>) => {
  const { id, type, plan, audio, language, host } = ev.data;
  const post = (m: WorkerResponse) => self.postMessage(m);
  if (host) env.remoteHost = host;
  try {
    const { run, device } = await load(plan, (progress) => post({ id, type: 'progress', progress }));
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
