// The methodologies in the administration mode: the explorer, a published methodology's editor and its panes.
import { expect, test, type Page } from '@playwright/test';
import { enterMode, openTool } from './session';

/** Opens the latest version of a methodology from the explorer of the admin mode. */
async function openMethodology(page: Page, name: string): Promise<void> {
  await enterMode(page, 'admin');
  await openTool(page, 'Methodologies');
  const tree = page.getByRole('tree', { name: 'Methodologies' });
  await page.getByRole('searchbox', { name: 'Filter methodologies' }).fill(name);
  const group = tree.getByRole('treeitem', { level: 1 }).filter({ has: page.getByText(name, { exact: true }) });
  if ((await group.getAttribute('aria-expanded')) !== 'true') await group.click();
  await expect(group).toHaveAttribute('aria-expanded', 'true');
  // its versions follow it, the latest first
  await group.locator('xpath=following-sibling::*[@role="treeitem"][1]').click();
  await expect(page).toHaveURL(new RegExp(`/methodology\\?.*name=${name}`));
}

test('the admin mode lists the methodologies and opens one', async ({ page }) => {
  await page.goto('/');
  await openMethodology(page, 'sdlc');
  await expect(page.getByText('Published version: it is immutable.')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'General' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'New version' })).toBeVisible();
});

test('the panes of a methodology list its elements', async ({ page }) => {
  await page.goto('/');
  await openMethodology(page, 'sdlc');
  await page.getByRole('tab', { name: /^Processes/ }).click();
  await expect(page.getByRole('tab', { name: /^Processes/ })).toHaveAttribute('aria-selected', 'true');
});

test('a methodology route reopens its tab', async ({ page }) => {
  await page.goto('/');
  await openMethodology(page, 'sdlc');
  const url = page.url();
  await page.reload();
  await expect(page).toHaveURL(url);
  await expect(page.getByText('Published version: it is immutable.')).toBeVisible();
});
