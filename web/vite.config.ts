import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Évite de dépendre de @types/node pour une seule variable d'environnement.
declare const process: { env: Record<string, string | undefined> };

const gateway = process.env.GOAP_GATEWAY_URL ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [svelte()],
  server: {
    proxy: {
      // Toutes les RPC Connect (/goap.<pkg>.v1.<Service>/<Method>) vont à la passerelle.
      '^/goap\\.': { target: gateway, changeOrigin: true },
    },
  },
});
