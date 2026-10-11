import assert from 'node:assert/strict';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';

const bundle = await build({
  configFile: false,
  logLevel: 'silent',
  build: {
    write: false,
    minify: false,
    rollupOptions: {
      preserveEntrySignatures: 'strict',
      input: fileURLToPath(new URL('./visual.ts', import.meta.url)),
      output: { format: 'es' },
    },
  },
});
const script = bundle.output.find((entry) => entry.type === 'chunk').code;
const { visualCaptureEnabled, resolveCSS, cssReferences, completeVisualCapture, fitVisualCapture } =
  await import(`data:text/javascript;base64,${Buffer.from(script).toString('base64')}`);

test('site policy uses DNS boundaries and most specific parent with hard disable', () => {
  const policy = {
    visual_mode: 'opt-out',
    visual_sites: { 'example.com': false, 'docs.example.com': true },
  };
  assert.equal(visualCaptureEnabled('https://www.example.com', policy), false);
  assert.equal(visualCaptureEnabled('https://api.docs.example.com', policy), true);
  assert.equal(visualCaptureEnabled('https://notexample.com', policy), true);
  assert.equal(visualCaptureEnabled('https://example.com./', policy), false);
  assert.equal(visualCaptureEnabled('file:///tmp/page.html', policy), false);
  assert.equal(
    visualCaptureEnabled('https://docs.example.com', { ...policy, visual_mode: 'disabled' }),
    false,
  );
});

test('CSS keeps conditions while resolving imports, fonts and background images', () => {
  const css =
    '@import "theme.css" screen and (min-width: 600px); @media (prefers-color-scheme: dark) { .x { background: url(../bg.png); mask: url(#icon); } } @font-face { src: url("font.woff2"); }';
  const resolved = resolveCSS(css, 'https://example.com/css/main.css');
  assert.match(
    resolved,
    /@import "https:\/\/example.com\/css\/theme.css" screen and \(min-width: 600px\)/,
  );
  assert.match(resolved, /@media \(prefers-color-scheme: dark\)/);
  assert.match(resolved, /mask: url\(#icon\)/);
  assert.deepEqual(
    cssReferences(css, 'https://example.com/css/main.css').map(({ url, kind }) => ({ url, kind })),
    [
      { url: 'https://example.com/css/theme.css', kind: 'css' },
      { url: 'https://example.com/bg.png', kind: 'image' },
      { url: 'https://example.com/css/font.woff2', kind: 'font' },
    ],
  );
});

test('asset completion follows CSS imports once and preserves downloaded image bytes', async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  const bytes = Buffer.from([1, 2, 3, 4]);
  globalThis.fetch = async (url, options) => {
    calls.push(url);
    assert.equal(options.credentials, 'include');
    if (url.endsWith('main.css'))
      return new Response('@import "nested.css"; .x{background:url("pic.png")}', {
        headers: { 'content-type': 'text/css' },
      });
    if (url.endsWith('nested.css'))
      return new Response('@import "main.css";', { headers: { 'content-type': 'text/css' } });
    return new Response(bytes, { headers: { 'content-type': 'image/png' } });
  };
  try {
    const capture = await completeVisualCapture({
      resources: [{ url: 'https://example.com/main.css', mime: '', kind: 'css', data: '' }],
    });
    assert.equal(calls.length, 3);
    assert.equal(new Set(calls).size, 3);
    const image = capture.resources.find((asset) => asset.kind === 'image');
    assert.deepEqual(Buffer.from(image.data, 'base64'), bytes);
    const css = Buffer.from(capture.resources[0].data, 'base64').toString();
    assert.match(css, /https:\/\/example.com\/nested.css/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('local capture does not fetch remote URLs and handles unreadable assets', async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url) => {
    calls.push(url);
    throw new Error('Unavailable');
  };
  try {
    const capture = await completeVisualCapture(
      {
        resources: [
          { url: 'https://example.com/pic.png', mime: '', kind: 'image', data: '' },
          { url: 'blob:https://example.com/local', mime: '', kind: 'image', data: '' },
        ],
      },
      true,
    );
    assert.deepEqual(calls, ['blob:https://example.com/local']);
    assert.ok(capture.resources.every((asset) => !asset.data));
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('assets reject excessive bytes and HTML disguised as images', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) =>
    url.endsWith('large.png')
      ? new Response('', {
          headers: { 'content-type': 'image/png', 'content-length': String(9 * 1024 * 1024) },
        })
      : new Response('<html>Login</html>', { headers: { 'content-type': 'text/html' } });
  try {
    const capture = await completeVisualCapture({
      resources: ['large.png', 'login.png'].map((name) => ({
        url: `https://example.com/${name}`,
        mime: '',
        kind: 'image',
        data: '',
      })),
    });
    assert.ok(capture.resources.every((asset) => !asset.data));
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('JSON byte budget accounts for base64 and original HTML without changing search data', () => {
  const document = {
    html: 'Indexed HTML',
    text: 'Indexed text',
    visual: {
      html: 'Visual HTML',
      resources: [
        {
          url: 'https://example.com/image.png',
          mime: 'image/png',
          kind: 'image',
          data: 'A'.repeat(1000),
        },
        { url: 'https://example.com/style.css', mime: 'text/css', kind: 'css', data: 'AAAA' },
      ],
    },
  };
  fitVisualCapture(document, 300);
  assert.ok(Buffer.byteLength(JSON.stringify(document)) <= 300);
  assert.equal(document.visual.resources.length, 1);
  assert.equal(document.visual.resources[0].kind, 'css');
  assert.equal(document.html, 'Indexed HTML');
  assert.equal(document.text, 'Indexed text');
  fitVisualCapture(document, 20);
  assert.equal(document.visual, undefined);
  assert.equal(document.html, 'Indexed HTML');
});

test('decoded resource budget includes UTF-8 visual HTML and resource URLs', async () => {
  const size = 8 * 1024 * 1024;
  const capture = await completeVisualCapture(
    {
      html: 'é'.repeat(size / 2),
      resources: [0, 1, 2].map((index) => ({
        url: `https://example.com/${index}.png`,
        kind: 'image',
        mime: 'image/png',
        data: Buffer.alloc(size).toString('base64'),
      })),
    },
    true,
  );
  assert.equal(capture.resources.filter((asset) => asset.data).length, 1);
  assert.ok(
    Buffer.byteLength(capture.html) +
      capture.resources.reduce(
        (total, asset) =>
          total + Buffer.from(asset.data, 'base64').length + Buffer.byteLength(asset.url),
        0,
      ) <=
      24 * 1024 * 1024,
  );
});

test('legacy font MIME types normalize instead of rejecting an entire archive', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () =>
    new Response('font bytes', {
      headers: { 'content-type': 'application/x-font-woff' },
    });
  try {
    const capture = await completeVisualCapture({
      html: '',
      resources: [{ url: 'https://example.com/font.woff', kind: 'font', mime: '', data: '' }],
    });
    assert.equal(capture.resources[0].mime, 'font/woff');
    assert.ok(capture.resources[0].data);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('binary font responses normalize for offline replay', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () =>
    new Response('font bytes', { headers: { 'content-type': 'binary/octet-stream' } });
  try {
    const capture = await completeVisualCapture({
      html: '',
      resources: [{ url: 'https://example.com/font.woff2', kind: 'font', mime: '', data: '' }],
    });
    assert.equal(capture.resources[0].mime, 'font/woff2');
    assert.equal(Buffer.from(capture.resources[0].data, 'base64').toString(), 'font bytes');
  } finally {
    globalThis.fetch = originalFetch;
  }
});
