// What the tests share: the person they sign in as and the steps every test takes.
import { expect, type Page } from '@playwright/test';

declare const process: { env: Record<string, string | undefined> };

/**
 * The person the tests act as. On fresh data (make web-e2e) the first account created is the administrator, so the
 * tests see the administration too; against a stack of yours, name an administrator with GOAP_E2E_SUBJECT /
 * GOAP_E2E_PASSWORD.
 */
export const subject = process.env.GOAP_E2E_SUBJECT ?? 'e2e-admin';
export const password = process.env.GOAP_E2E_PASSWORD ?? 'e2e-password';

/** The demo project (platform/devseed/demo.json), seeded by make web-e2e (GOAP_GRAPH_SEED=demo). */
export const demoProject = { key: 'PROJ-DEMO', name: 'Online commerce' };

/** Signs in from the sign-in screen, creating the account when it does not exist yet. */
export async function signIn(page: Page): Promise<void> {
  await page.goto('/');
  const card = page.locator('form.card');
  await card.getByLabel('Subject').fill(subject);
  await card.getByLabel('Password', { exact: true }).fill(password);
  await card.getByRole('button', { name: 'Sign in', exact: true }).click();
  const refused = card.getByRole('alert');
  const shell = page.getByRole('radiogroup', { name: 'Mode' });
  await expect(shell.or(refused)).toBeVisible();
  if (await refused.isVisible()) {
    // no such account: create it (the first of fresh data is the administrator)
    await card.getByRole('tab', { name: 'Create account' }).click();
    await card.getByLabel('Password', { exact: true }).fill(password);
    await card.getByLabel('Confirm the password').fill(password);
    await card.getByRole('button', { name: 'Create account', exact: true }).click();
  }
  await expect(shell).toBeVisible();
}

/** Makes a project the active one through the header's selector. */
export async function selectProject(page: Page, name: string): Promise<void> {
  await page.locator('button.proj').click();
  await page.getByRole('dialog', { name: 'Project' }).getByRole('button', { name: new RegExp(`^${name}\\b`) }).click();
  await expect(page.locator('button.proj')).toContainText(name);
}

/** Enters a mode of the header (project, change, navigation, admin). */
export async function enterMode(page: Page, mode: 'project' | 'change' | 'navigation' | 'admin'): Promise<void> {
  const button = page.locator(`[role=radio][data-mode=${mode}]`);
  await button.click();
  await expect(button).toHaveAttribute('aria-checked', 'true');
}

/** Shows a tool of the left bar (Changes, Methodologies…), unless it is shown already. */
export async function openTool(page: Page, title: string): Promise<void> {
  const button = page.getByRole('toolbar', { name: 'Navigation tools' }).getByRole('button', { name: title, exact: true });
  if ((await button.getAttribute('aria-pressed')) !== 'true') await button.click();
  await expect(button).toHaveAttribute('aria-pressed', 'true');
}
