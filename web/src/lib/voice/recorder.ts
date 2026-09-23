// Push-to-talk capture: microphone -> 16 kHz mono Float32 samples (what Whisper expects).
// Audio stays in memory; nothing is uploaded or persisted.

export const SAMPLE_RATE = 16000;

export function voiceSupported(): boolean {
  return typeof navigator !== 'undefined' && !!navigator.mediaDevices?.getUserMedia && typeof MediaRecorder !== 'undefined';
}

export class Recorder {
  private stream?: MediaStream;
  private rec?: MediaRecorder;
  private chunks: Blob[] = [];
  private started = 0;

  /** Opens the microphone and starts recording. Rejects if permission is denied. */
  async start(): Promise<void> {
    this.stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
    });
    this.chunks = [];
    this.rec = new MediaRecorder(this.stream);
    this.rec.ondataavailable = (e) => {
      if (e.data.size) this.chunks.push(e.data);
    };
    this.started = Date.now();
    this.rec.start();
  }

  /** Stops recording, releases the microphone and returns the decoded samples (empty if too short). */
  async stop(): Promise<Float32Array> {
    const rec = this.rec;
    if (!rec) return new Float32Array();
    const done = new Promise<void>((resolve) => {
      rec.onstop = () => resolve();
    });
    if (rec.state !== 'inactive') rec.stop();
    await done;
    this.release();
    if (Date.now() - this.started < 300 || !this.chunks.length) return new Float32Array();
    const blob = new Blob(this.chunks, { type: rec.mimeType });
    this.chunks = [];
    const ctx = new AudioContext({ sampleRate: SAMPLE_RATE });
    try {
      const buf = await ctx.decodeAudioData(await blob.arrayBuffer());
      return buf.getChannelData(0).slice();
    } finally {
      void ctx.close();
    }
  }

  /** Aborts without producing samples. */
  cancel(): void {
    if (this.rec && this.rec.state !== 'inactive') {
      this.rec.onstop = null;
      this.rec.stop();
    }
    this.chunks = [];
    this.release();
  }

  private release(): void {
    this.stream?.getTracks().forEach((t) => t.stop());
    this.stream = undefined;
    this.rec = undefined;
  }
}
