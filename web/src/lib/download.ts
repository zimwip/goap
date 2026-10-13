/** Hands a blob to the browser as a file to save. */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Hands a text to the browser as a file to save. */
export function downloadText(body: string, filename: string, type: string): void {
  downloadBlob(new Blob([body], { type }), filename);
}
