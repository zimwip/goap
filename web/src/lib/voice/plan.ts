// Which Whisper model the browser runs, and in which format: chosen from what the device can do. Pure, so the
// rules are tested; `probe` reads the device (main thread or worker).
import type { VoiceModel } from './settings.svelte';

export type ModelSize = 'tiny' | 'base' | 'small';
export type Device = 'webgpu' | 'wasm';
export type DType = string | Record<string, string>;

/** What the device offers. */
export interface Capabilities {
  webgpu: boolean;
  /** the WebGPU adapter computes in fp16 (`shader-f16`) */
  f16: boolean;
  /** GiB of memory, when the browser tells (`navigator.deviceMemory`, Chromium only) */
  memory?: number;
  /** logical cores */
  cores?: number;
}

/** What the worker loads. */
export interface Plan {
  size: ModelSize;
  model: string;
  device: Device;
  dtype: DType;
}

export const MODEL_IDS: Record<ModelSize, string> = {
  tiny: 'Xenova/whisper-tiny',
  base: 'Xenova/whisper-base',
  small: 'onnx-community/whisper-small',
};

/** Rough download of each size in the format the plan picks, for the settings and the button's title. */
export const MODEL_MB: Record<ModelSize, number> = { tiny: 40, base: 80, small: 250 };

/** The size `auto` picks: small where it runs about real time and fits 250 MB (WebGPU with fp16), base on a capable
 *  CPU, tiny on a weak one. */
export function autoSize(c: Capabilities): ModelSize {
  if (c.webgpu && c.f16 && (c.memory === undefined || c.memory >= 4)) return 'small';
  if ((c.memory !== undefined && c.memory < 4) || (c.cores !== undefined && c.cores < 4)) return 'tiny';
  return 'base';
}

/** The format of a size on the device: q8 on the CPU; on WebGPU a q4 decoder with the encoder in fp16 when the
 *  adapter has it, else fp32. Small's fp32 encoder alone would be about 350 MB, so small without fp16 runs on the CPU
 *  in q8 instead. */
export function format(size: ModelSize, device: Device, f16: boolean): { device: Device; dtype: DType } {
  if (device === 'webgpu' && (f16 || size !== 'small'))
    return { device, dtype: { encoder_model: f16 ? 'fp16' : 'fp32', decoder_model_merged: 'q4' } };
  return { device: 'wasm', dtype: 'q8' };
}

export function plan(choice: VoiceModel, c: Capabilities): Plan {
  const size = choice === 'auto' ? autoSize(c) : choice;
  return { size, model: MODEL_IDS[size], ...format(size, c.webgpu ? 'webgpu' : 'wasm', c.f16) };
}

/** The same model on the CPU, when WebGPU fails to build it. */
export function onCPU(p: Plan): Plan {
  return { ...p, device: 'wasm', dtype: 'q8' };
}

interface GPUish {
  requestAdapter(): Promise<{ features: { has(f: string): boolean } } | null>;
}

/** Reads what the device offers; never throws. */
export async function probe(): Promise<Capabilities> {
  const nav = (typeof navigator !== 'undefined' ? navigator : {}) as Navigator & { gpu?: GPUish; deviceMemory?: number };
  const c: Capabilities = { webgpu: false, f16: false, memory: nav.deviceMemory, cores: nav.hardwareConcurrency || undefined };
  try {
    const adapter = await nav.gpu?.requestAdapter();
    if (adapter) {
      c.webgpu = true;
      c.f16 = adapter.features.has('shader-f16');
    }
  } catch {
    // no WebGPU
  }
  return c;
}
