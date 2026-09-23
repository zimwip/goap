import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Avoids depending on @types/node for a single environment variable.
declare const process: { env: Record<string, string | undefined> };

const gateway = process.env.GOAP_GATEWAY_URL ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [svelte()],
  server: {
    proxy: {
      // All Connect RPCs (/goap.<pkg>.v1.<Service>/<Method>) go to the gateway.
      // http-proxy forwards streamed responses (WatchEvents) without buffering them.
      '^/goap\\.': { target: gateway, changeOrigin: true },
      // Gateway HTTP endpoints (platform status…).
      '/api': { target: gateway, changeOrigin: true },
    },
  },
});
