/// <reference types="svelte" />
/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Base for the Jaeger UI ("Trace" links), default http://localhost:16686. */
  readonly VITE_GOAP_JAEGER_URL?: string;
  /** Optional base for RPC URLs (default: relative, via the Vite proxy). */
  readonly VITE_GOAP_BASE_URL?: string;
}
