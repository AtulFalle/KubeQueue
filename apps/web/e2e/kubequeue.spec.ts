import { expect, test } from '@playwright/test';

test('submits a Job and opens its durable detail', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('link', { name: 'Submit job' }).click();
  await page.getByLabel('Name').fill(`e2e-${Date.now()}`);
  await page.getByLabel('Namespace').fill('default');
  await page.getByRole('button', { name: 'Add to queue' }).click();

  await expect(page).toHaveURL(/\/jobs\/[0-9a-f-]+$/);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'History' })).toBeVisible();
});

test('persists inventory filters in the URL', async ({ page }) => {
  await page.goto('/');
  await page.getByPlaceholder('Job name').fill('e2e');

  await expect(page).toHaveURL(/search=e2e/);
});
