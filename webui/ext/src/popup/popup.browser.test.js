import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { readFile } from 'node:fs/promises';
import { extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';
import { build } from 'vite';

const extensionRoot = fileURLToPath(new URL('../..', import.meta.url));
let browser;

before(async () => {
  await build({ root: extensionRoot, logLevel: 'silent' });
  browser = await chromium.launch({ headless: true });
});

after(async () => {
  await browser?.close();
});

async function popup(options = {}) {
  const page = await browser.newPage({ viewport: { width: 320, height: 850 } });
  await page.route('http://popup.test/**', async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    const contentType = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' }[
      extname(pathname)
    ];
    const body = await readFile(resolve(extensionRoot, 'dist', pathname.slice(1)));
    await route.fulfill({ contentType: contentType || 'application/octet-stream', body });
  });
  await page.route('**/api/profile', (route) =>
    route.fulfill({ contentType: 'application/json', body: '{"user_id":0}' }),
  );
  await page.addInitScript((settings) => {
    window.createdTabs = [];
    window.runtimeMessages = [];
    window.chrome = {
      storage: {
        local: {
          get: (_keys, callback) =>
            callback({
              histerURL: settings.serverURL || 'http://127.0.0.1:4433/',
              histerToken: 'secret-token',
            }),
          set: async () => {},
        },
      },
      action: { getBadgeText: (_details, callback) => callback('') },
      tabs: {
        query: (_details, callback) =>
          callback([{ id: 1, url: settings.pageURL || 'https://example.com/visited#section' }]),
        sendMessage: async () => {
          if (settings.noContentScript) throw new Error('No content script');
          return { url: settings.canonicalURL || 'https://example.com/canonical?value=a&b=two' };
        },
        create: async (details) => {
          if (settings.failCreate) throw new Error('Tab creation failed');
          window.createdTabs.push(details);
        },
      },
      runtime: {
        sendMessage: (request, callback) => {
          window.runtimeMessages.push(request);
          const result = { isSkipped: false };
          callback?.(result);
          return Promise.resolve(result);
        },
      },
    };
  }, options);
  await page.goto('http://popup.test/popup.html');
  return page;
}

test('popup opens the canonical saved preview without indexing or exposing a token', async () => {
  const page = await popup({ serverURL: 'https://hister.example/archive?old=parameter#fragment' });
  try {
    await page.getByRole('button', { name: 'Show this page in Hister', exact: true }).click();
    const tabs = await page.evaluate(() => window.createdTabs);
    assert.equal(tabs.length, 1);
    const target = new URL(tabs[0].url);
    assert.equal(target.origin, 'https://hister.example');
    assert.equal(target.pathname, '/archive/preview');
    assert.equal(target.searchParams.get('id'), 'https://example.com/canonical?value=a&b=two');
    assert.deepEqual([...target.searchParams.keys()], ['id']);
    assert.equal(target.hash, '');
    assert.ok(!target.href.includes('secret-token'));
    const requests = await page.evaluate(() => window.runtimeMessages);
    assert.ok(!requests.some((request) => request.action === 'indexCurrentPage'));
    const button = page.getByRole('button', { name: 'Show this page in Hister', exact: true });
    assert.ok(await button.isEnabled());
    assert.ok(await button.isVisible());
  } finally {
    await page.close();
  }
});

test('popup uses the visited URL when a content script is unavailable', async () => {
  const page = await popup({ noContentScript: true });
  try {
    await page.getByRole('button', { name: 'Show this page in Hister', exact: true }).click();
    const [target] = await page.evaluate(() => window.createdTabs);
    assert.equal(new URL(target.url).pathname, '/preview');
    assert.equal(new URL(target.url).searchParams.get('id'), 'https://example.com/visited#section');
  } finally {
    await page.close();
  }
});

test('popup disables saved preview for browser-internal pages', async () => {
  const page = await popup({ pageURL: 'chrome://extensions/', noContentScript: true });
  try {
    const button = page.getByRole('button', { name: 'Show this page in Hister', exact: true });
    await button.waitFor();
    assert.ok(await button.isDisabled());
    assert.deepEqual(await page.evaluate(() => window.createdTabs), []);
  } finally {
    await page.close();
  }
});

test('popup reports failed tab creation and allows retry', async () => {
  const page = await popup({ failCreate: true });
  try {
    const button = page.getByRole('button', { name: 'Show this page in Hister', exact: true });
    await button.click();
    await page.getByText('Tab creation failed', { exact: true }).waitFor();
    assert.ok(await button.isEnabled());
  } finally {
    await page.close();
  }
});
