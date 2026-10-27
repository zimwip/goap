// A change made by hand in the demo project: the form of the changes explorer, then the change tab and change mode.
import { expect, test } from '@playwright/test';
import { demoProject, openTool, selectProject } from './session';

test('a change created by hand opens in its tab', async ({ page }) => {
  const title = `E2E change ${Date.now()}`;
  await page.goto('/');
  await selectProject(page, demoProject.name);
  await openTool(page, 'Changes');
  await page.getByRole('button', { name: 'New change' }).click();
  await page.getByLabel('Title').fill(title);
  await page.getByLabel('Intent').fill('Checked by the end-to-end tests of the web');
  await page.getByLabel('Methodology').selectOption('sdlc');
  await page.getByRole('button', { name: 'Create the change' }).click();

  await expect(page).toHaveURL(/\/change\?.*id=/);
  await expect(page.getByRole('tab', { name: /^Change / })).toBeVisible();
  // the new change is the working change: the page enters change mode on it
  await expect(page.locator('button.wc')).toHaveAttribute('title', new RegExp(title));
  await expect(page.locator('[role=radio][data-mode=change]')).toHaveAttribute('aria-checked', 'true');
  // its tab follows the lifecycle of its methodology, from its initial state
  await expect(page.getByText('Lifecycle', { exact: true })).toBeVisible();
});
