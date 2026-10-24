import { describe, expect, it } from 'vitest';
import { autoSize, onCPU, plan } from './plan';

describe('voice plan', () => {
  it('picks small on WebGPU with fp16', () => {
    const p = plan('auto', { webgpu: true, f16: true, memory: 8, cores: 8 });
    expect(p).toEqual({
      size: 'small',
      model: 'onnx-community/whisper-small',
      device: 'webgpu',
      dtype: { encoder_model: 'fp16', decoder_model_merged: 'q4' },
    });
  });

  it('picks base on WebGPU without fp16, keeping the fp32 encoder', () => {
    const p = plan('auto', { webgpu: true, f16: false });
    expect(p.size).toBe('base');
    expect(p.dtype).toEqual({ encoder_model: 'fp32', decoder_model_merged: 'q4' });
  });

  it('picks base on a capable CPU and tiny on a weak one', () => {
    expect(autoSize({ webgpu: false, f16: false, memory: 8, cores: 8 })).toBe('base');
    expect(autoSize({ webgpu: false, f16: false })).toBe('base');
    expect(autoSize({ webgpu: false, f16: false, memory: 2, cores: 8 })).toBe('tiny');
    expect(autoSize({ webgpu: false, f16: false, cores: 2 })).toBe('tiny');
    expect(autoSize({ webgpu: true, f16: true, memory: 2 })).toBe('tiny');
  });

  it('runs an explicit small on the CPU in q8 when the GPU has no fp16', () => {
    expect(plan('small', { webgpu: true, f16: false })).toMatchObject({ size: 'small', device: 'wasm', dtype: 'q8' });
    expect(plan('small', { webgpu: false, f16: false })).toMatchObject({ device: 'wasm', dtype: 'q8' });
  });

  it('keeps an explicit choice and falls back to the CPU with the same model', () => {
    const p = plan('tiny', { webgpu: true, f16: true });
    expect(p).toMatchObject({ size: 'tiny', device: 'webgpu' });
    expect(onCPU(p)).toMatchObject({ size: 'tiny', model: 'Xenova/whisper-tiny', device: 'wasm', dtype: 'q8' });
  });
});
