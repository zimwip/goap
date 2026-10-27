// The sign-in screen of the local mode (ADR 0040), seen without a session.
import { expect, test } from '@playwright/test';
import { subject } from './session';

test.use({ storageState: { cookies: [], origins: [] } });

test('without a session the sign-in screen replaces the shell', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'GOAP' })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Sign in' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('radiogroup', { name: 'Mode' })).toHaveCount(0);
});

test('a wrong password is refused', async ({ page }) => {
  await page.goto('/');
  await page.getByLabel('Subject').fill(subject);
  await page.getByLabel('Password', { exact: true }).fill('not-the-password');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('Wrong subject or password.');
});

test('creating an account checks the two passwords', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('tab', { name: 'Create account' }).click();
  await page.getByLabel('Password', { exact: true }).fill('long-enough');
  await page.getByLabel('Confirm the password').fill('something-else');
  await expect(page.getByText('The two passwords differ.')).toBeVisible();
});
