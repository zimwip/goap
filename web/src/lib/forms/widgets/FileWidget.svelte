<script lang="ts">
  // A file (or several) the person attaches: read in the page, its content sent in base64 with the values.
  import type { WidgetProps } from '../registry';
  import type { FileValue } from '../types';
  import { MAX_FILE_BYTES, strings } from '../validate';

  let { id, field, value, onchange, disabled, invalid }: WidgetProps = $props();
  const multiple = $derived(field.constraints?.['multiple'] === true);
  const files = $derived<FileValue[]>(Array.isArray(value) ? (value as FileValue[]) : value && typeof value === 'object' ? [value as FileValue] : []);
  const accept = $derived(strings(field.constraints?.['accept']).join(','));
  const limit = $derived(Math.min(typeof field.constraints?.['maxBytes'] === 'number' ? (field.constraints['maxBytes'] as number) : MAX_FILE_BYTES, MAX_FILE_BYTES));
  let error = $state('');

  const size = (n = 0) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / (1 << 20)).toFixed(1)} MiB`);

  function read(file: File): Promise<FileValue> {
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => {
        const url = String(r.result ?? '');
        resolve({ name: file.name, type: file.type || undefined, size: file.size, content: url.slice(url.indexOf(',') + 1) });
      };
      r.onerror = () => reject(r.error);
      r.readAsDataURL(file);
    });
  }

  async function pick(list: FileList | null) {
    error = '';
    const chosen = [...(list ?? [])];
    const big = chosen.find((f) => f.size > limit);
    if (big) {
      error = `${big.name} is larger than ${size(limit)}`;
      return;
    }
    const read_ = await Promise.all(chosen.map(read));
    if (multiple) onchange([...files, ...read_]);
    else onchange(read_[0]);
  }
  function drop(i: number) {
    const rest = files.filter((_, j) => j !== i);
    onchange(multiple ? rest : undefined);
  }
</script>

<div class="files">
  {#each files as f, i (i)}
    <span class="file">{f.name} <span class="muted">{size(f.size)}</span>
      {#if !disabled}<button type="button" class="small" aria-label={`Remove ${f.name}`} onclick={() => drop(i)}>×</button>{/if}</span>
  {/each}
  {#if multiple || !files.length}
    <input {id} type="file" accept={accept || undefined} multiple={multiple} {disabled} aria-invalid={invalid} onchange={(e) => pick(e.currentTarget.files)} />
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
</div>

<style>
  .files {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    align-items: flex-start;
  }
  .file {
    display: inline-flex;
    gap: 0.4rem;
    align-items: center;
  }
  .muted {
    color: var(--muted);
    font-size: 0.8rem;
  }
  .err {
    color: var(--danger);
    font-size: 0.8rem;
    margin: 0;
  }
</style>
