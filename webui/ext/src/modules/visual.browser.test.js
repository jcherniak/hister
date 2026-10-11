import assert from 'node:assert/strict';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';
import { build } from 'vite';

test(
  'browser capture preserves current CSS rules, conditions and image sources',
  { timeout: 30000 },
  async () => {
    const bundle = await build({
      configFile: false,
      logLevel: 'silent',
      build: {
        write: false,
        minify: false,
        rollupOptions: {
          input: fileURLToPath(new URL('./visual.ts', import.meta.url)),
          preserveEntrySignatures: 'strict',
          output: { format: 'es' },
        },
      },
    });
    const script = bundle.output.find((entry) => entry.type === 'chunk').code;
    const browser = await chromium.launch({ headless: true });
    try {
      const page = await browser.newPage({ viewport: { width: 1200, height: 800 } });
      await page.route('**/*', async (route) => {
        const url = route.request().url();
        if (url === 'https://example.com/') {
          await route.fulfill({
            contentType: 'text/html',
            body: `<!doctype html><html><head>
          <style>@media(min-width:600px){.sample{background:url('/background.svg')}}
          @font-face{font-family:test;src:url('/font.woff2')}</style>
          <style id="disabled">.must-not-capture{color:red}</style>
          <link rel="stylesheet" href="https://other.example/style.css" media="screen">
          </head><body><input value="private" type="password"><textarea>private</textarea>
          <picture><source srcset="/selected.svg"><img src="/fallback.svg"></picture>
          <img id="local"><div class="sample">Sample</div><a href="next">Live link</a><a href="#sample">Fragment</a><script>window.active=true</script>
          </body></html>`,
          });
        } else if (url.endsWith('.css')) {
          await route.fulfill({ contentType: 'text/css', body: '.sample{color:blue}' });
        } else {
          await route.fulfill({
            contentType: url.endsWith('.woff2') ? 'font/woff2' : 'image/svg+xml',
            body: '<svg xmlns="http://www.w3.org/2000/svg" width="100" height="50"></svg>',
          });
        }
      });
      await page.goto('https://example.com/');
      await page.evaluate(() => {
        document.styleSheets[0].insertRule('.runtime{color:red}', 0);
        document.querySelector('#disabled').sheet.disabled = true;
        const sheet = new CSSStyleSheet({ media: 'screen' });
        sheet.replaceSync('@media(prefers-color-scheme:dark){body{color:white}}');
        document.adoptedStyleSheets = [sheet];
        document.querySelector('#local').src = URL.createObjectURL(
          new Blob(['<svg xmlns="http://www.w3.org/2000/svg" width="50" height="30"></svg>'], {
            type: 'image/svg+xml',
          }),
        );
      });
      await page.waitForFunction(() => [...document.images].every((image) => image.complete));
      const result = await page.evaluate(async (source) => {
        const module = await import(`data:text/javascript;base64,${btoa(source)}`);
        return module.captureVisual(location.href);
      }, script);
      assert.equal(result.viewport_width, 1200);
      assert.equal(result.viewport_height, 800);
      assert.match(result.html, /runtime/);
      assert.match(result.html, /@media/);
      assert.match(result.html, /prefers-color-scheme/);
      assert.doesNotMatch(result.html, /private|<script|must-not-capture|srcset=/);
      assert.match(result.html, /media="screen"/);
      assert.match(result.html, /href="https:\/\/example.com\/next"/);
      assert.match(result.html, /href="#sample"/);
      assert.match(result.html, /blob:hister-visual\/0/);
      assert.ok(
        result.resources.some(
          (asset) => asset.url === 'https://other.example/style.css' && !asset.data,
        ),
      );
      assert.ok(
        result.resources.some((asset) => asset.url === 'https://example.com/background.svg'),
      );
      assert.ok(
        result.resources.some(
          (asset) => asset.url === 'https://example.com/font.woff2' && asset.kind === 'font',
        ),
      );
      assert.ok(
        result.resources.some(
          (asset) =>
            asset.url === 'https://example.com/selected.svg' &&
            asset.width === 100 &&
            asset.height === 50,
        ),
      );
      assert.ok(
        result.resources.some((asset) => asset.url === 'blob:hister-visual/0' && asset.data),
      );
      assert.doesNotMatch(result.html, /blob:https:\/\/example.com/);
      assert.equal(
        await page.evaluate(async (source) => {
          const module = await import(`data:text/javascript;base64,${btoa(source)}`);
          return module.captureVisual('https://example.com/changed');
        }, script),
        undefined,
      );
    } finally {
      await browser.close();
    }
  },
);
