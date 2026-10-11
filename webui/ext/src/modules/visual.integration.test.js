import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';
import { build } from 'vite';

const binary = process.env.HISTER_TEST_BINARY && resolve(process.env.HISTER_TEST_BINARY);
const token = 'visual-integration-fixture';

async function bundleModule(name) {
  const bundle = await build({
    configFile: false,
    logLevel: 'silent',
    build: {
      write: false,
      minify: false,
      rollupOptions: {
        input: fileURLToPath(new URL(`./${name}.ts`, import.meta.url)),
        preserveEntrySignatures: 'strict',
        output: { format: 'es' },
      },
    },
  });
  return bundle.output.find((entry) => entry.type === 'chunk').code;
}

async function startServer(directory, maxBodySize) {
  const listener = createServer();
  await new Promise((resolve) => listener.listen(0, '127.0.0.1', resolve));
  const port = listener.address().port;
  await new Promise((resolve) => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  const config = join(directory, 'config.json');
  await writeFile(
    config,
    JSON.stringify({
      app: { directory: join(directory, 'data'), access_token: token, user_handling: false },
      server: { address: `127.0.0.1:${port}`, base_url: base, max_batch_body_size: maxBodySize },
      visual_archive: { mode: 'opt-out' },
    }),
  );
  const child = spawn('timeout', ['120', binary, '--config', config, 'listen'], {
    cwd: directory,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let logs = '';
  child.stdout.on('data', (data) => {
    logs += data;
  });
  child.stderr.on('data', (data) => {
    logs += data;
  });
  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null) throw new Error(`Isolated server exited: ${logs}`);
    try {
      const response = await fetch(`${base}/api/rules`, { headers: { 'X-Access-Token': token } });
      if (response.ok) return { child, base, config };
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  child.kill('SIGTERM');
  throw new Error(`Isolated server did not start: ${logs}`);
}

function search(server, query) {
  return JSON.parse(
    execFileSync(
      'timeout',
      ['20', binary, '--config', server.config, 'search', '--format', 'json', query],
      { encoding: 'utf8' },
    ),
  );
}

test(
  'capture, server storage and sandbox replay preserve CSS without changing search',
  {
    skip: !binary && 'Set HISTER_TEST_BINARY to a built Hister binary.',
    timeout: 110000,
  },
  async () => {
    const directory = await mkdtemp(join(tmpdir(), 'hister-visual-integration-'));
    const servers = [];
    let browser;
    const originalFetch = globalThis.fetch;
    const originalChrome = globalThis.chrome;
    try {
      const normalDir = await mkdtemp(join(directory, 'normal-'));
      const limitedDir = await mkdtemp(join(directory, 'limited-'));
      const normal = await startServer(normalDir, 40);
      servers.push(normal);
      const limited = await startServer(limitedDir, 1);
      servers.push(limited);
      const source = await bundleModule('visual');
      browser = await chromium.launch({ headless: true });
      const page = await browser.newPage({ viewport: { width: 1200, height: 800 } });
      const pageURL = 'https://visual.example.test/captured';
      const font = await readFile(
        fileURLToPath(
          new URL(
            '../../../../node_modules/@fontsource/inter/files/inter-latin-400-normal.woff2',
            import.meta.url,
          ),
        ),
      );
      await page.route('https://visual.example.test/**', async (route) => {
        const url = route.request().url();
        if (url === pageURL) {
          await route.fulfill({
            contentType: 'text/html',
            body: `<!doctype html><html><head>
          <link rel="stylesheet" href="/main.css" media="screen"></head><body>
          <h1 id="marker">visualonlymarker</h1><img src="/image.svg"><img id="raster">
          <script>window.archivedScript=true</script></body></html>`,
          });
        } else if (url.endsWith('.css')) {
          await route.fulfill({
            contentType: 'text/css',
            body: '@font-face{font-family:ArchiveFont;src:url("/font.woff2")} #marker{font-family:ArchiveFont;font-weight:400} @media(min-width:900px){#marker{color:rgb(12,34,56)}} @media(max-width:899px){#marker{color:rgb(65,43,21)}}',
          });
        } else if (url.endsWith('.woff2')) {
          await route.fulfill({ contentType: 'binary/octet-stream', body: font });
        } else {
          await route.fulfill({
            contentType: 'image/svg+xml',
            body: '<svg xmlns="http://www.w3.org/2000/svg" width="100" height="50"><rect width="100" height="50" fill="blue"/></svg>',
          });
        }
      });
      await page.goto(pageURL);
      await page.evaluate(() => {
        const canvas = document.createElement('canvas');
        canvas.width = 640;
        canvas.height = 480;
        const context = canvas.getContext('2d');
        const pixels = context.createImageData(canvas.width, canvas.height);
        let seed = 123456;
        for (let offset = 0; offset < pixels.data.length; offset += 4) {
          for (let channel = 0; channel < 3; channel++) {
            seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
            pixels.data[offset + channel] = seed >>> 24;
          }
          pixels.data[offset + 3] = 255;
        }
        context.putImageData(pixels, 0, 0);
        document.querySelector('#raster').src = canvas.toDataURL('image/png');
      });
      await page.waitForFunction(
        () => document.images[0].naturalWidth === 100 && document.images[1].naturalWidth === 640,
      );
      const visual = await page.evaluate(async (source) => {
        const module = await import(`data:text/javascript;base64,${btoa(source)}`);
        const capture = await module.captureVisual(location.href);
        return module.completeVisualCapture(capture);
      }, source);
      assert.equal(visual.resources.filter((asset) => asset.data).length, 4);
      assert.equal(visual.resources.find((asset) => asset.kind === 'font').mime, 'font/woff2');
      assert.ok(
        Buffer.from(visual.resources.find((asset) => asset.width === 640).data, 'base64').length >
          100000,
      );
      const document = {
        url: pageURL,
        title: 'Integration sample',
        text: 'normalindexmarker searchable article text',
        html: '<html><body>normalindexmarker searchable article text</body></html>',
        visual,
      };
      const added = await fetch(`${normal.base}/api/add`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Access-Token': token,
          Origin: 'hister://',
        },
        body: JSON.stringify(document),
      });
      assert.equal(added.status, 201, await added.text());
      const preview = await fetch(
        `${normal.base}/api/visual-preview?url=${encodeURIComponent(pageURL)}`,
        { headers: { 'X-Access-Token': token } },
      );
      assert.equal(preview.status, 200, await preview.clone().text());
      assert.match(preview.headers.get('content-type'), /ndjson/);
      const events = (await preview.text())
        .trim()
        .split('\n')
        .map((line) => JSON.parse(line));
      assert.ok(events.some((event) => event.type === 'progress'));
      const complete = events.find((event) => event.type === 'complete');
      assert.ok(complete);
      assert.doesNotMatch(complete.html, /<script/);
      assert.match(complete.html, /@media/);
      assert.match(complete.html, /data:image\/svg\+xml;base64,/);
      assert.match(complete.html, /data:font\/woff2;base64,/);
      await page.setContent('<iframe sandbox="" width="1200" height="800"></iframe>');
      await page.locator('iframe').evaluate((iframe, html) => {
        iframe.srcdoc = html;
      }, complete.html);
      const frame = page.frames().find((frame) => frame.parentFrame());
      await frame.waitForFunction(() => document.images[0]?.naturalWidth === 100);
      await frame.waitForFunction(() => document.images[1]?.naturalWidth === 640);
      assert.deepEqual(
        await frame.evaluate(async () => {
          const fonts = await document.fonts.load('16px ArchiveFont');
          return fonts.map((font) => ({ family: font.family, status: font.status }));
        }),
        [{ family: 'ArchiveFont', status: 'loaded' }],
      );
      assert.deepEqual(
        await frame.evaluate(() => [
          document.images[0].naturalWidth,
          document.images[0].naturalHeight,
        ]),
        [100, 50],
      );
      assert.deepEqual(
        await frame
          .locator('#raster')
          .evaluate((image) => [image.naturalWidth, image.naturalHeight]),
        [640, 480],
      );
      assert.equal(
        await frame.locator('#marker').evaluate((element) => getComputedStyle(element).color),
        'rgb(12, 34, 56)',
      );
      await page.locator('iframe').evaluate((iframe) => {
        iframe.width = '600';
      });
      assert.equal(
        await frame.locator('#marker').evaluate((element) => getComputedStyle(element).color),
        'rgb(65, 43, 21)',
      );
      assert.equal(await frame.evaluate(() => window.archivedScript), undefined);
      assert.match(JSON.stringify(search(normal, 'normalindexmarker')), /visual\.example\.test/);
      assert.doesNotMatch(
        JSON.stringify(search(normal, 'visualonlymarker')),
        /visual\.example\.test/,
      );

      const networkSource = await bundleModule('network');
      const { sendPageData } = await import(
        `data:text/javascript;base64,${Buffer.from(networkSource).toString('base64')}`
      );
      globalThis.chrome = {
        storage: { local: { get: (_keys, callback) => callback({ histerToken: token }) } },
      };
      const statuses = [];
      globalThis.fetch = async (...args) => {
        const response = await originalFetch(...args);
        if (String(args[0]).endsWith('/api/add')) statuses.push(response.status);
        return response;
      };
      const oversized = {
        ...document,
        url: 'https://visual.example.test/fallback',
        visual: { ...visual, html: `<html><body>${'oversized '.repeat(150000)}</body></html>` },
      };
      assert.equal(
        (
          await sendPageData(`${limited.base}/api/add`, oversized, [
            { name: 'Origin', value: 'hister://' },
          ])
        ).status,
        201,
      );
      assert.deepEqual(statuses, [413, 201]);
      assert.equal(oversized.visual, undefined);
      assert.match(JSON.stringify(search(limited, 'normalindexmarker')), /fallback/);
      assert.equal(
        (
          await originalFetch(
            `${limited.base}/api/visual-preview?url=${encodeURIComponent(oversized.url)}`,
            { headers: { 'X-Access-Token': token } },
          )
        ).status,
        404,
      );
    } finally {
      globalThis.fetch = originalFetch;
      globalThis.chrome = originalChrome;
      await browser?.close();
      await Promise.all(
        servers.map(async ({ child }) => {
          child.kill('SIGTERM');
          if (child.exitCode === null) await new Promise((resolve) => child.once('exit', resolve));
        }),
      );
      await rm(directory, { recursive: true, force: true });
    }
  },
);
