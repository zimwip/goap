/// <reference types="svelte" />
/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Base de l'interface Jaeger (liens « Trace »), défaut http://localhost:16686. */
  readonly VITE_GOAP_JAEGER_URL?: string;
  /** Base optionnelle des URL RPC (par défaut : relative, via le proxy Vite). */
  readonly VITE_GOAP_BASE_URL?: string;
}
