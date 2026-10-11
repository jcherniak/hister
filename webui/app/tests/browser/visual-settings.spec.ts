import { expect, test, type Page } from '@playwright/test';
import { mockAPI } from './fixtures';

type Rules = {
  allow: string[];
  skip: string[];
  priority: string[];
  versioning: string[];
  aliases: Record<string, string>;
  visual_mode?: string;
  visual_sites: Record<string, boolean>;
  visual_rules: Record<string, string>;
  visual_can_configure_install: boolean;
};

const shared = '^https://shared\\.example\\.com/';
const visualOnly = '^https://visual\\.example\\.com/';

async function visualRules(page: Page, overrides: Partial<Rules> = {}) {
  await mockAPI(page);
  const rules: Rules = {
    allow: [],
    skip: [],
    priority: [],
    versioning: [],
    aliases: {},
    visual_mode: 'opt-out',
    visual_sites: {},
    visual_rules: {},
    visual_can_configure_install: true,
    ...overrides,
  };
  const submissions: URLSearchParams[] = [];
  await page.route('**/api/rules', async (route) => {
    if (route.request().method() === 'POST') {
      const form = new URLSearchParams(route.request().postData() ?? '');
      submissions.push(form);
      for (const list of ['allow', 'skip', 'priority', 'versioning'] as const) {
        if (form.has(list)) rules[list] = form.get(list)!.split('\n').filter(Boolean);
      }
      if (form.has('visual_mode')) rules.visual_mode = form.get('visual_mode')!;
      if (form.has('visual_rules')) rules.visual_rules = JSON.parse(form.get('visual_rules')!);
      await route.fulfill({ status: 200 });
    } else {
      await route.fulfill({ json: rules });
    }
  });
  await page.route('**/api/rules/preview', async (route) => {
    const draft = route.request().postDataJSON();
    await route.fulfill({ json: { pattern: draft.pattern ?? '' } });
  });
  await page.goto('/rules');
  return submissions;
}

async function draftRule(page: Page, pattern: string) {
  const form = page.locator('#add-rule-section');
  if (!(await form.evaluate((node) => (node as HTMLDetailsElement).open))) {
    await form.locator('summary').click();
  }
  await form.getByRole('button', { name: 'Advanced regexp', exact: true }).click();
  await form.getByLabel('Regexp pattern', { exact: true }).fill(pattern);
  return form;
}

function visualRulesOf(submission: URLSearchParams) {
  return JSON.parse(submission.get('visual_rules')!);
}

test('installation mode saves without a separate site editor', async ({ page }) => {
  const submissions = await visualRules(page);
  const section = page.locator('#visual-archive');
  await expect(section.getByLabel('Hostname', { exact: true })).toHaveCount(0);
  await section.getByLabel('Installation mode').selectOption('opt-in');
  await section.getByRole('button', { name: 'Save mode', exact: true }).click();
  await expect(section.getByRole('status')).toHaveText('Visual archive settings saved');
  expect(submissions[0].get('visual_mode')).toBe('opt-in');
  expect(submissions[0].has('skip')).toBe(false);
  expect(submissions[0].has('visual_rules')).toBe(false);
});

test('nonadministrator cannot change the installation mode', async ({ page }) => {
  await visualRules(page, { visual_can_configure_install: false });
  const section = page.locator('#visual-archive');
  await expect(section.getByLabel('Installation mode')).toBeDisabled();
  await expect(section.getByRole('button', { name: 'Save mode', exact: true })).toHaveCount(0);
});

test('rules table shows visual options and removes metadata only for orphaned patterns', async ({
  page,
}) => {
  const submissions = await visualRules(page, {
    skip: [shared],
    priority: [shared],
    visual_rules: { [shared]: 'opt-out', [visualOnly]: 'opt-in' },
  });
  const table = page.locator('#indexing-rules');
  await expect(table.getByRole('columnheader', { name: 'Visual archive' })).toBeVisible();
  const rows = table.locator('tbody tr');
  await expect(rows).toHaveCount(3);
  await expect(rows.filter({ hasText: visualOnly })).toContainText('Opt-in');
  await expect(rows.filter({ hasText: shared })).toHaveCount(2);
  await expect(rows.filter({ hasText: shared }).first()).toContainText('Opt-out');
  await expect(rows.filter({ hasText: shared }).last()).toContainText('Opt-out');
  await rows.filter({ hasText: 'skip' }).getByRole('button').nth(1).click();
  await expect(rows).toHaveCount(2);
  expect(submissions[0].get('skip')).toBe('');
  expect(visualRulesOf(submissions[0])).toEqual({ [shared]: 'opt-out', [visualOnly]: 'opt-in' });
  await rows.filter({ hasText: 'priority' }).getByRole('button').nth(1).click();
  await expect(rows).toHaveCount(1);
  expect(visualRulesOf(submissions[1])).toEqual({ [visualOnly]: 'opt-in' });
  await rows.first().getByRole('button').nth(1).click();
  await expect(rows).toHaveCount(0);
  expect(visualRulesOf(submissions[2])).toEqual({});
});

test('adds visual-only rules and visual options on indexing rules', async ({ page }) => {
  const submissions = await visualRules(page);
  const rows = page.locator('#indexing-rules tbody tr');
  const form = await draftRule(page, visualOnly);
  await form.getByLabel('Action', { exact: true }).selectOption('visual');
  const add = form.locator('button:has-text("Add rule")');
  await expect(add).toBeDisabled();
  await form.getByLabel('Visual archive', { exact: true }).selectOption('opt-in');
  await add.click();
  await expect(rows).toHaveCount(1);
  expect(submissions[0].get('skip')).toBe('');
  expect(submissions[0].get('allow')).toBe('');
  expect(visualRulesOf(submissions[0])).toEqual({ [visualOnly]: 'opt-in' });
  await expect(form.getByLabel('Visual archive', { exact: true })).toHaveValue('');
  await draftRule(page, shared);
  await form.getByLabel('Action', { exact: true }).selectOption('skip');
  await form.getByLabel('Visual archive', { exact: true }).selectOption('opt-out');
  await add.click();
  await expect(rows).toHaveCount(2);
  expect(submissions[1].get('skip')).toBe(shared);
  expect(visualRulesOf(submissions[1])).toEqual({ [visualOnly]: 'opt-in', [shared]: 'opt-out' });
});

test('bulk adds visual-only rules', async ({ page }) => {
  const submissions = await visualRules(page);
  const form = page.locator('#add-rule-section');
  await form.locator('summary').click();
  await form.getByRole('button', { name: 'Bulk add', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Bulk add rules' });
  await dialog.getByLabel('Patterns', { exact: true }).fill(`${shared}\n${visualOnly}`);
  await dialog.getByLabel('Type', { exact: true }).selectOption('visual');
  const add = dialog.getByRole('button', { name: 'Add 2 rules', exact: true });
  await expect(add).toBeDisabled();
  await dialog.getByLabel('Visual archive', { exact: true }).selectOption('opt-out');
  await add.click();
  await expect(dialog).toHaveCount(0);
  expect(submissions[0].get('skip')).toBe('');
  expect(visualRulesOf(submissions[0])).toEqual({ [shared]: 'opt-out', [visualOnly]: 'opt-out' });
  await expect(page.locator('#indexing-rules tbody tr')).toHaveCount(2);
});

test('editing keeps visual options and moves them on rename', async ({ page }) => {
  const renamed = '^https://renamed\\.example\\.com/';
  const submissions = await visualRules(page, {
    skip: [shared],
    visual_rules: { [shared]: 'opt-out', [visualOnly]: 'opt-in' },
  });
  const rows = page.locator('#indexing-rules tbody tr');
  const editor = rows.filter({ has: page.locator('#edit-rule-pattern') });
  await rows.filter({ hasText: 'skip' }).getByRole('button').first().click();
  await expect(editor.getByLabel('Visual archive', { exact: true })).toHaveValue('opt-out');
  await editor.getByLabel('Regexp pattern', { exact: true }).fill(renamed);
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(rows.filter({ hasText: renamed })).toContainText('Opt-out');
  expect(submissions[0].get('skip')).toBe(renamed);
  expect(visualRulesOf(submissions[0])).toEqual({ [renamed]: 'opt-out', [visualOnly]: 'opt-in' });
  await rows.filter({ hasText: visualOnly }).getByRole('button').first().click();
  await editor.getByLabel('Visual archive', { exact: true }).selectOption('');
  await expect(editor.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled();
  await editor.getByLabel('Visual archive', { exact: true }).selectOption('opt-out');
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(rows.filter({ hasText: visualOnly })).toContainText('Opt-out');
  expect(submissions[1].get('skip')).toBe(renamed);
  expect(visualRulesOf(submissions[1])).toEqual({ [renamed]: 'opt-out', [visualOnly]: 'opt-out' });
});

test('disabled mode hides visual controls and preserves saved metadata', async ({ page }) => {
  const submissions = await visualRules(page, {
    visual_mode: 'disabled',
    skip: [shared],
    visual_rules: { [shared]: 'opt-out', [visualOnly]: 'opt-in' },
  });
  const table = page.locator('#indexing-rules');
  await expect(table.getByRole('columnheader', { name: 'Visual archive' })).toHaveCount(0);
  const rows = table.locator('tbody tr');
  await expect(rows).toHaveCount(1);
  const form = page.locator('#add-rule-section');
  await form.locator('summary').click();
  await expect(form.getByLabel('Visual archive', { exact: true })).toHaveCount(0);
  await expect(form.getByLabel('Action', { exact: true }).locator('option')).toHaveCount(4);
  await rows.first().getByRole('button').nth(1).click();
  await expect(rows).toHaveCount(0);
  expect(submissions[0].get('skip')).toBe('');
  expect(visualRulesOf(submissions[0])).toEqual({ [visualOnly]: 'opt-in' });
});

test('disabled mode keeps a hidden visual setting on an edited normal rule', async ({ page }) => {
  const renamed = '^https://renamed\\.example\\.com/';
  const submissions = await visualRules(page, {
    visual_mode: 'disabled',
    skip: [shared],
    visual_rules: { [shared]: 'opt-out', [visualOnly]: 'opt-in' },
  });
  const rows = page.locator('#indexing-rules tbody tr');
  await rows.first().getByRole('button').first().click();
  const editor = rows.filter({ has: page.locator('#edit-rule-pattern') });
  await expect(editor.getByLabel('Visual archive', { exact: true })).toHaveCount(0);
  await editor.getByLabel('Regexp pattern', { exact: true }).fill(renamed);
  await editor.getByLabel('Action', { exact: true }).selectOption('priority');
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(rows.filter({ hasText: renamed })).toContainText('priority');
  expect(visualRulesOf(submissions[0])).toEqual({ [renamed]: 'opt-out', [visualOnly]: 'opt-in' });
});
