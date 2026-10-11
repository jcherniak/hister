import assert from 'node:assert/strict';
import { test } from 'node:test';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';

const bundle = await build({
  configFile: false,
  logLevel: 'silent',
  build: {
    write: false,
    minify: false,
    rolldownOptions: {
      input: fileURLToPath(new URL('./background.ts', import.meta.url)),
      output: { format: 'iife' },
    },
  },
});
const script = bundle.output.find((entry) => entry.type === 'chunk').code;

function browser({
  rules = {},
  contentScript = true,
  indexingEnabled = true,
  sourceURL = 'https://example.com/visited?tracking=true',
  visual = {
    html: '<html><body>Visual only</body></html>',
    resources: [],
    viewport_width: 1200,
    viewport_height: 800,
    color_scheme: 'light',
  },
  submissionStatuses = [],
  visualPolicy = () => true,
  policyStatus = 200,
} = {}) {
  const canonicalURL = 'https://example.com/clean';
  const documents = [];
  const lookups = [];
  const captures = [];
  const policyChecks = [];
  let onMessage;
  let onActivated;
  let onHeadersReceived;
  const storage = {
    histerURL: 'https://hister.example/',
    showIndexedBadge: true,
    indexingEnabled,
  };
  const event = { addListener() {} };
  const action = (_, callback) => callback?.();
  vm.runInNewContext(script, {
    URL,
    URLSearchParams,
    TextEncoder,
    TextDecoder,
    atob,
    btoa,
    AbortSignal,
    console,
    chrome: {
      runtime: {
        onMessage: { addListener: (fn) => (onMessage = fn) },
        getURL: (path) => `chrome-extension://hister/${path}`,
      },
      tabs: {
        onRemoved: event,
        onUpdated: event,
        onActivated: { addListener: (fn) => (onActivated = fn) },
        get: async () => ({ id: 1, url: sourceURL }),
        sendMessage: async (tabId, request, options) => {
          assert.equal(tabId, 1);
          assert.equal(options.frameId, 0);
          if (!contentScript) throw new Error('No content script');
          if (request.action === 'captureVisual') {
            captures.push(request.url);
            return { visual };
          }
          assert.equal(request.action, 'getPageURL');
          return { url: canonicalURL };
        },
      },
      storage: {
        onChanged: event,
        local: {
          get: async (_, callback) => {
            const data = structuredClone(storage);
            callback?.(data);
            return data;
          },
        },
      },
      action: {
        setBadgeText: action,
        setBadgeBackgroundColor: action,
        setIcon: action,
      },
      webRequest: {
        onHeadersReceived: {
          addListener: (fn, filter) => {
            assert.equal(String(filter.types), 'main_frame');
            onHeadersReceived = fn;
          },
        },
      },
    },
    fetch: async (url, options) => {
      const parsed = new URL(url);
      if (parsed.protocol === 'chrome-extension:') throw new Error('No icon in test');
      if (parsed.pathname === '/api/rules') return Response.json(rules);
      if (parsed.pathname === '/api/rules/preview') {
        const request = JSON.parse(options.body);
        assert.equal(request.visual_policy, true);
        policyChecks.push(request.url);
        return Response.json({ visual_enabled: visualPolicy(request.url) }, { status: policyStatus });
      }
      if (parsed.pathname === '/api/add') {
        documents.push(JSON.parse(options.body));
        return new Response('', { status: submissionStatuses[documents.length - 1] ?? 201 });
      }
      if (parsed.pathname === '/api/document') {
        lookups.push(parsed.searchParams.get('url'));
        return new Response('', { status: 200 });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    },
  });
  const message = (request) =>
    new Promise((resolve) =>
      onMessage(request, { url: sourceURL, tab: { id: 1, url: sourceURL } }, resolve),
    );
  return {
    sourceURL,
    canonicalURL,
    documents,
    lookups,
    captures,
    policyChecks,
    message,
    activate: () => onActivated({ tabId: 1 }),
    respond: (statusCode, url = sourceURL, tabId = 1) =>
      onHeadersReceived({ tabId, url, statusCode }),
    submit: (manual = false) =>
      message({
        pageData: { url: canonicalURL, title: 'Article', text: 'Article text', faviconURL: '' },
        ...(manual ? { action: 'reindex' } : {}),
      }),
  };
}

test('visual collection is opt-out, opt-in, or hard-disabled without changing indexed text', async () => {
  for (const [visual_mode, visual_sites, enabled] of [
    ['opt-out', {}, true],
    ['opt-out', { 'example.com': false }, false],
    ['opt-in', {}, false],
    ['opt-in', { 'example.com': true }, true],
    ['disabled', { 'example.com': true }, false],
    [undefined, {}, false],
  ]) {
    const b = browser({ rules: { visual_mode, visual_sites } });
    assert.equal((await b.submit()).status_code, 201);
    assert.equal(b.captures.length, Number(enabled));
    assert.equal(Boolean(b.documents[0].visual), enabled);
    assert.equal(b.documents[0].text, 'Article text');
  }
});

test('manual indexing never overrides visual exclusions', async () => {
  const b = browser({ rules: { visual_mode: 'opt-out', skip: ['/visited'] } });
  assert.equal((await b.submit(true)).status_code, 201);
  assert.equal(b.captures.length, 0);
  assert.equal(b.documents[0].visual, undefined);
});

test('visual pattern rules use the server matcher for visited and canonical URLs', async () => {
  for (const excluded of [undefined, '/visited', '/clean']) {
    const b = browser({
      rules: { visual_mode: 'opt-in', visual_rules: { '(?i)example': 'opt-in' } },
      visualPolicy: (url) => !excluded || !url.includes(excluded),
    });
    assert.equal((await b.submit()).status_code, 201);
    assert.deepEqual(b.policyChecks.sort(), [b.sourceURL, b.canonicalURL].sort());
    assert.equal(b.captures.length, excluded ? 0 : 1);
    assert.equal(b.documents[0].text, 'Article text');
  }
});

test('failed visual pattern policy checks omit capture but keep normal indexing', async () => {
  const b = browser({
    rules: { visual_mode: 'opt-out', visual_rules: { '(?i)private': 'opt-out' } },
    policyStatus: 500,
  });
  assert.equal((await b.submit()).status_code, 201);
  assert.equal(b.captures.length, 0);
  assert.equal(b.documents[0].visual, undefined);
});

test('installation disable prevents visual policy checks and capture', async () => {
  const b = browser({ rules: { visual_mode: 'disabled', visual_rules: { 'example': 'opt-in' } } });
  assert.equal((await b.submit()).status_code, 201);
  assert.equal(b.policyChecks.length, 0);
  assert.equal(b.captures.length, 0);
});

test('failed visual capture does not stop normal indexing', async () => {
  const b = browser({ rules: { visual_mode: 'opt-out' }, contentScript: false });
  assert.equal((await b.submit()).status_code, 201);
  assert.equal(b.documents[0].visual, undefined);
});

test('oversized or rate-limited visual submissions retry normal indexing once', async () => {
  for (const status of [413, 429]) {
    const b = browser({ rules: { visual_mode: 'opt-out' }, submissionStatuses: [status, 201] });
    assert.equal((await b.submit()).status_code, 201);
    assert.equal(b.documents.length, 2);
    assert.ok(b.documents[0].visual);
    assert.equal(b.documents[1].visual, undefined);
    assert.equal(b.documents[1].text, 'Article text');
    assert.equal(b.captures.length, 1);
  }
});

test('a known sensitive rejection prevents later visual collection', async () => {
  const b = browser({ rules: { visual_mode: 'opt-out' }, submissionStatuses: [422, 201] });
  assert.equal((await b.submit()).status_code, 422);
  assert.equal((await b.submit()).status_code, 201);
  assert.equal(b.captures.length, 1);
  assert.equal(b.documents[1].visual, undefined);
});

test('automatic canonical submissions respect rules for both visited and canonical URLs', async () => {
  const cases = [
    { rules: {}, expected: 201 },
    { rules: { skip: ['/visited'] }, expected: 406 },
    { rules: { skip: ['/clean'] }, expected: 406 },
    { rules: { allow: ['/visited'] }, expected: 406 },
    { rules: { allow: ['/clean'] }, expected: 406 },
    { rules: { allow: ['example\\.com'] }, expected: 201 },
  ];
  for (const { rules, expected } of cases) {
    const b = browser({ rules });
    const response = await b.submit();
    assert.equal(response.status_code, expected, JSON.stringify(rules));
    assert.equal(b.documents.length, expected === 201 ? 1 : 0);
    if (b.documents.length) assert.equal(b.documents[0].url, b.canonicalURL);
  }
});

test('SPA submissions use the captured source URL instead of the initial document URL', async () => {
  const b = browser({
    sourceURL: 'https://example.com/?temporary-chat=true',
    rules: { skip: ['temporary-chat'], visual_mode: 'opt-out' },
  });
  for (const [sourceURL, expected] of [
    ['https://example.com/c/normal', 201],
    ['https://example.com/?temporary-chat=true', 406],
  ]) {
    const response = await b.message({
      sourceURL,
      pageData: { url: b.canonicalURL, title: 'Chat', text: 'Chat text', faviconURL: '' },
    });
    assert.equal(response.status_code, expected);
  }
  assert.equal(b.documents.length, 1);
  assert.equal(b.captures.length, 1);
  assert.equal('sourceURL' in b.documents[0], false);
});

test('manual canonical submissions still bypass rules for the visited URL', async () => {
  const b = browser({ rules: { skip: ['/visited', '/clean'] } });
  const response = await b.submit(true);
  assert.equal(response.status_code, 201);
  assert.equal(b.documents[0].url, b.canonicalURL);
  assert.equal(b.documents[0].metadata.ignore_skip_rules, true);
});

test('visited URL rules continue to ignore fragments', async () => {
  const b = browser({
    sourceURL: 'https://example.com/visited#section',
    rules: { allow: ['/visited$', '/clean$'] },
  });
  assert.equal((await b.submit()).status_code, 201);
});

test('indexed badges look up the canonical URL even with automatic indexing disabled', async () => {
  for (const indexingEnabled of [true, false]) {
    const b = browser({ indexingEnabled });
    await b.activate();
    assert.deepEqual(b.lookups, [b.canonicalURL]);
  }
});

test('indexed badges fall back to the visited URL when no content script is available', async () => {
  const b = browser({ contentScript: false });
  await b.activate();
  assert.deepEqual(b.lookups, [b.sourceURL]);
});

test('automatic submissions are rejected for unsuccessful navigation responses', async () => {
  const cases = [
    { name: 'success', responses: [[200]], expected: 201 },
    { name: 'not found', responses: [[404]], expected: 404 },
    { name: 'server error', responses: [[503]], expected: 503 },
    // 304 renders a valid cached page and a redirect is superseded by its target.
    { name: 'not modified', responses: [[304]], expected: 201 },
    { name: 'redirect to success', responses: [[301], [200]], expected: 201 },
    { name: 'redirect to error', responses: [[301], [404]], expected: 404 },
    // The status of another page, tab, or of the canonical URL that was never
    // navigated to must not reject the submission.
    { name: 'other url', responses: [[404, 'https://example.com/other']], expected: 201 },
    { name: 'canonical url', responses: [[404, 'https://example.com/clean']], expected: 201 },
    { name: 'other tab', responses: [[404, undefined, 2]], expected: 201 },
    { name: 'no response seen', responses: [], expected: 201 },
  ];
  for (const { name, responses, expected } of cases) {
    const b = browser();
    for (const response of responses) b.respond(...response);
    const result = await b.submit();
    assert.equal(result.status_code, expected, name);
    assert.equal(b.documents.length, expected === 201 ? 1 : 0, name);
  }
});

// A page submits a final snapshot while it is being replaced, so its status
// must survive the responses of the navigation that evicts it.
test('an error page stays rejected after the tab navigated away', async () => {
  const b = browser();
  b.respond(404);
  for (const url of ['https://example.com/a', 'https://example.com/b', 'https://example.com/c']) {
    b.respond(200, url);
  }
  assert.equal((await b.submit()).status_code, 404);
  assert.equal(b.documents.length, 0);
});

test('the navigation status ignores fragments and manual indexing overrides it', async () => {
  const b = browser({ sourceURL: 'https://example.com/visited#section' });
  b.respond(404, 'https://example.com/visited');
  assert.equal((await b.submit()).status_code, 404);
  assert.equal(b.documents.length, 0);
  assert.equal((await b.submit(true)).status_code, 201);
  assert.equal(b.documents.length, 1);
});

test('popup rule checks include both the visited and canonical URLs', async () => {
  for (const pattern of ['/visited', '/clean']) {
    const b = browser({ rules: { skip: [pattern] } });
    const response = await b.message({
      action: 'checkSkipRule',
      url: b.canonicalURL,
      sourceURL: b.sourceURL,
    });
    assert.equal(response.isSkipped, true);
  }
});
