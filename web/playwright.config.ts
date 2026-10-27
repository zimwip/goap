// End-to-end tests of the IDE (Playwright): the browser drives the web of a running platform. `make web-e2e` starts the
// local stack on fresh data with the IDE (goap-local e2e) and runs them; against a stack already running (make dev),
// `make web-e2e-run`, or `npx playwright test` with GOAP_E2E_URL. The files are `e2e/*.e2e.ts`: vitest leaves them alone.
import { defineConfig, devices } from '@playwright/test';

// Avoids depending on @types/node for a few environment variables.
declare const process: { env: Record<string, string | undefined> };

export default defineConfig({
  testDir: './e2e',
  testMatch: /.*\.e2e\.ts$/,
  outputDir: '../.goap/e2e/results',
  // the tests share one platform and the person signed in: one worker, in order
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: [['list'], ['html', { outputFolder: '../.goap/e2e/report', open: 'never' }]],
  use: {
    baseURL: process.env.GOAP_E2E_URL ?? 'http://localhost:5173',
    viewport: { width: 1400, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    // signs the person in once and keeps the session for the others (e2e/auth.setup.e2e.ts)
    { name: 'setup', testMatch: /auth\.setup\.e2e\.ts$/ },
    {
      name: 'chromium',
      testIgnore: /auth\.setup\.e2e\.ts$/,
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'], viewport: { width: 1400, height: 900 }, storageState: '../.goap/e2e/session.json' },
    },
  ],
});
