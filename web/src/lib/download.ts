/** Hands a blob to the browser as a file to save. */
function downloadBlob(blob: Blob, filename: string): void {
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

/** Hands base64 bytes (a protobuf `bytes` field over JSON) to the browser as a file to save. */
export function downloadBase64(data: string, filename: string, type: string): void {
  const bin = atob(data);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  downloadBlob(new Blob([bytes], { type }), filename);
}

/** Saves an export of the graph (ADR 0104, 0111): the JSON document, or the OKF zip archive. */
export function downloadGraphExport(res: { document?: string; archive?: string; filename?: string; mediaType?: string }, fallback: string): void {
  if (res.archive) downloadBase64(res.archive, res.filename || `${fallback}.okf.zip`, res.mediaType || 'application/zip');
  else downloadText(res.document ?? '', res.filename || `${fallback}.json`, res.mediaType || 'application/json');
}
