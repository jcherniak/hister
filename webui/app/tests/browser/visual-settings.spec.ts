import { expect, test, type Page } from '@playwright/test';
import { mockAPI } from './fixtures';

async function visualRules(page: Page, canConfigure = true) {
  await mockAPI(page);
  const rules = {
    allow: [],
    skip: [],
    priority: [],
    versioning: [],
    aliases: {},
    visual_mode: 'opt-out',
    visual_sites: {} as Record<string, boolean>,
    visual_can_configure_install: canConfigure,
  };
  const submissions: URLSearchParams[] = [];
  await page.route('**/api/rules', async (route) => {
    if (route.request().method() === 'POST') {
      const form = new URLSearchParams(route.request().postData() ?? '');
      submissions.push(form);
      if (form.has('visual_mode')) rules.visual_mode = form.get('visual_mode')!;
      if (form.has('visual_sites')) rules.visual_sites = JSON.parse(form.get('visual_sites')!);
      await route.fulfill({ status: 200 });
    } else {
      await route.fulfill({ json: rules });
    }
  });
  await page.goto('/rules');
  return submissions;
}

test('visual settings select installation policy and site overrides independently', async ({
  page,
}) => {
  const submissions = await visualRules(page);
  const section = page.locator('#visual-archive');
  await section.getByLabel('Installation mode').selectOption('opt-in');
  await section.getByRole('button', { name: 'Save mode', exact: true }).click();
  await expect(section.getByRole('status')).toHaveText('Visual archive settings saved');
  expect(submissions[0].get('visual_mode')).toBe('opt-in');
  expect(submissions[0].has('skip')).toBe(false);
  await section.getByLabel('Hostname', { exact: true }).fill('docs.example.com');
  await section.getByRole('combobox', { name: 'Visual capture', exact: true }).selectOption('true');
  await section.getByRole('button', { name: 'Save site', exact: true }).click();
  await expect(section.locator('code')).toHaveText('docs.example.com');
  expect(JSON.parse(submissions[1].get('visual_sites')!)).toEqual({ 'docs.example.com': true });
  expect(submissions[1].has('visual_mode')).toBe(false);
  await section.getByRole('button', { name: 'Use default', exact: true }).click();
  await expect(section.locator('code')).toHaveCount(0);
  expect(JSON.parse(submissions[2].get('visual_sites')!)).toEqual({});
});

test('nonadministrator can edit sites but not installation mode', async ({ page }) => {
  const submissions = await visualRules(page, false);
  const section = page.locator('#visual-archive');
  await expect(section.getByLabel('Installation mode')).toBeDisabled();
  await expect(section.getByRole('button', { name: 'Save mode', exact: true })).toHaveCount(0);
  await section.getByLabel('Hostname', { exact: true }).fill('private.example.com');
  await section
    .getByRole('combobox', { name: 'Visual capture', exact: true })
    .selectOption('false');
  await section.getByRole('button', { name: 'Save site', exact: true }).click();
  await expect(section.locator('code')).toHaveText('private.example.com');
  expect(JSON.parse(submissions[0].get('visual_sites')!)).toEqual({ 'private.example.com': false });
});
