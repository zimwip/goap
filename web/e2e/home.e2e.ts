// The shell once signed in: the project mode and its tasks dashboard, the project selector, the routes.
import { expect, test } from '@playwright/test';
import { demoProject, selectProject, subject } from './session';

test('a new page opens on the tasks of the project mode', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('[role=radio][data-mode=project]')).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('heading', { name: 'Tasks', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'My tasks' })).toBeVisible();
  await expect(page.getByRole('banner').or(page.locator('header')).first()).toContainText(subject);
});

test('the header selects the active project', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('button.proj')).toContainText('Select a project');
  await selectProject(page, demoProject.name);
});

test('the administrator is offered every mode', async ({ page }) => {
  await page.goto('/');
  for (const m of ['project', 'navigation', 'admin']) await expect(page.locator(`[role=radio][data-mode=${m}]`)).toBeEnabled();
  // change mode needs a working change
  await expect(page.locator('[role=radio][data-mode=change]')).toBeDisabled();
});

test('a link to a change that does not exist says so', async ({ page }) => {
  await page.goto('/change?id=change-00000000');
  await expect(page.getByRole('alert').filter({ hasText: 'not found' })).toBeVisible();
});
