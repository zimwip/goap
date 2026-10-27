// Signs the person in once; the other tests start from the session kept (playwright.config.ts, storageState).
import { test as setup } from '@playwright/test';
import { signIn } from './session';

setup('sign in', async ({ page }) => {
  await signIn(page);
  await page.context().storageState({ path: '../.goap/e2e/session.json' });
});
