// The OKF export of the graph (ADR 0111): the server streams a zip archive of one Markdown file per node while it
// renders it; the chunks are gathered as they come and handed to the browser as one file to save.
import { GRAPH_SERVICE, type GraphOkfChunk } from './api';
import { serverStream } from './stream';
import { downloadBlob } from './download';

/** Decodes a protobuf `bytes` field of Connect JSON (base64). */
export function fromBase64(data: string): Uint8Array<ArrayBuffer> {
  const bin = atob(data);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export interface OkfArchive {
  filename: string;
  mediaType: string;
  nodes: number;
  parts: Uint8Array<ArrayBuffer>[];
}

/** Gathers the messages of the stream: the first names the archive, the next ones carry its bytes. */
export function okfCollector(fallback: string): { archive: OkfArchive; take: (m: GraphOkfChunk) => void } {
  const archive: OkfArchive = { filename: fallback, mediaType: 'application/zip', nodes: 0, parts: [] };
  return {
    archive,
    take(m) {
      if (m.filename) archive.filename = m.filename;
      if (m.mediaType) archive.mediaType = m.mediaType;
      if (m.nodes) archive.nodes = m.nodes;
      if (m.chunk) archive.parts.push(fromBase64(m.chunk));
    },
  };
}

/** Streams the OKF tree of a namespace at a baseline (default: the head of the branch, main) and saves the zip;
 *  resolves to the number of nodes exported. */
export async function downloadGraphOkf(namespace: string, opts: { branch?: string; baselineId?: string; signal?: AbortSignal } = {}): Promise<number> {
  const { archive, take } = okfCollector(`graph-${namespace}.okf.zip`);
  await serverStream<{ namespace: string; branch?: string; baselineId?: string }, GraphOkfChunk>(
    GRAPH_SERVICE,
    'ExportGraphOkf',
    { namespace, branch: opts.branch, baselineId: opts.baselineId },
    take,
    opts.signal ?? new AbortController().signal,
  );
  downloadBlob(new Blob(archive.parts, { type: archive.mediaType }), archive.filename);
  return archive.nodes;
}
