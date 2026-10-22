// What the Graph tool of the navigation asks of the Graph explorer tab: start from the organisation, find a node to
// start from, or run a query. The tab takes each request once (`taken` follows `seq`, so a tab opened again replays
// nothing) and is opened by the request.
import { openTab } from "../shell/tabs.svelte";

export type GraphRequest =
  | { kind: "organisation" }
  | { kind: "find"; text: string }
  | { kind: "query"; text: string; run: boolean };

export const graphExplorer = $state<{
  seq: number;
  taken: number;
  request: GraphRequest | null;
}>({ seq: 0, taken: 0, request: null });

/** The request not taken yet, taken now. */
export function takeGraphRequest(): GraphRequest | null {
  if (graphExplorer.taken >= graphExplorer.seq) return null;
  graphExplorer.taken = graphExplorer.seq;
  return graphExplorer.request;
}

/** Opens the Graph explorer tab with a request for it. */
export function askGraphExplorer(request?: GraphRequest): void {
  if (request) {
    graphExplorer.request = request;
    graphExplorer.seq++;
  }
  openTab({ kind: "graphExplorer", params: {} }, { pin: true });
}
