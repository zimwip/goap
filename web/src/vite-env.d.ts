/// <reference types="svelte" />
/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Base optionnelle des URL RPC (par défaut : relative, via le proxy Vite). */
  readonly VITE_GOAP_BASE_URL?: string;
}
