import { expect, test } from '@playwright/test';
import { mockAPI, original, preview, previewPath } from './fixtures';

test.beforeEach(async ({ page }) => {
  await mockAPI(page);
  await page.route('**/api/preview?**', (route) =>
    route.fulfill({ json: { ...preview, visual_available: true } }),
  );
});

test('visual archive uses an isolated iframe and returns to readable content', async ({ page }) => {
  let requests = 0;
  await page.route('**/api/visual-preview?**', (route) => {
    requests++;
    const params = new URL(route.request().url()).searchParams;
    expect(params.get('url')).toBe(original);
    expect(params.get('document_id')).toBe('document-1');
    return route.fulfill({
      contentType: 'application/x-ndjson',
      body: `${JSON.stringify({ type: 'progress', completed: 1, total: 1, failed: 0 })}\n${JSON.stringify({ type: 'complete', html: '<html><body><p>Captured appearance</p><script>parent.document.body.innerHTML="unsafe"</script></body></html>', captured_at: '2026-10-10T12:00:00Z' })}\n`,
    });
  });
  await page.goto(previewPath());
  await expect(page.getByText('Saved article content')).toBeVisible();
  await page.getByRole('button', { name: 'Visual archive', exact: true }).click();
  const iframe = page.getByTitle('Visual archive', { exact: true });
  await expect(iframe).toHaveAttribute('sandbox', '');
  await expect(
    page.frameLocator('iframe[title="Visual archive"]').getByText('Captured appearance'),
  ).toBeVisible();
  await expect(page.getByRole('button', { name: 'Readable', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Readable', exact: true }).click();
  await expect(iframe).toHaveCount(0);
  await expect(page.getByText('Saved article content')).toBeVisible();
  expect(requests).toBe(1);
});

test('visual archive reports preparation failure and supports retry', async ({ page }) => {
  let attempts = 0;
  await page.route('**/api/visual-preview?**', (route) => {
    attempts++;
    return route.fulfill({
      contentType: 'application/x-ndjson',
      body:
        attempts === 1
          ? '{"type":"error","error":"Could not prepare the visual archive."}\n'
          : '{"type":"complete","html":"<p>Recovered capture</p>"}\n',
    });
  });
  await page.goto(previewPath());
  await page.getByRole('button', { name: 'Visual archive', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Could not prepare the visual archive.');
  await page.getByRole('button', { name: /retry/i }).click();
  await expect(
    page.frameLocator('iframe[title="Visual archive"]').getByText('Recovered capture'),
  ).toBeVisible();
  expect(attempts).toBe(2);
});

test('historical previews do not offer the latest visual archive', async ({ page }) => {
  await page.goto(previewPath(original, 7));
  await expect(page.getByText('Saved article content')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Visual archive', exact: true })).toHaveCount(0);
});

test('new-tab action opens the selected view and preserves the document', async ({
  page,
  context,
}) => {
  await mockAPI(context);
  await context.route('**/api/preview?**', (route) =>
    route.fulfill({ json: { ...preview, visual_available: true } }),
  );
  await context.route('**/api/visual-preview?**', (route) =>
    route.fulfill({
      contentType: 'application/x-ndjson',
      body: '{"type":"complete","html":"<p>New-tab capture</p>"}\n',
    }),
  );
  await page.goto(previewPath());
  for (const view of ['visual', 'readable'] as const) {
    await page
      .getByRole('button', { name: view === 'visual' ? 'Visual archive' : 'Readable', exact: true })
      .click();
    const link = page.getByRole('link', {
      name:
        view === 'visual' ? 'Open visual archive in a new tab' : 'Open readable view in a new tab',
    });
    const popupPromise = page.waitForEvent('popup');
    await link.click();
    const popup = await popupPromise;
    await expect.poll(() => new URL(popup.url()).searchParams.get('view')).toBe(view);
    const params = new URL(popup.url()).searchParams;
    expect(params.get('id')).toBe(original);
    expect(params.get('document_id')).toBe('document-1');
    if (view === 'visual') {
      await expect(
        popup.frameLocator('iframe[title="Visual archive"]').getByText('New-tab capture'),
      ).toBeVisible();
    } else {
      await expect(popup.getByText('Saved article content')).toBeVisible();
      await expect(popup.getByTitle('Visual archive', { exact: true })).toHaveCount(0);
    }
    await popup.close();
  }
});

test('a visual-view URL falls back to readable content without a capture', async ({ page }) => {
  await page.route('**/api/preview?**', (route) => route.fulfill({ json: preview }));
  await page.goto(`${previewPath()}&view=visual`);
  await expect(page.getByText('Saved article content')).toBeVisible();
  await expect(page.getByTitle('Visual archive', { exact: true })).toHaveCount(0);
});

test('visual archive renders inline image dimensions and conditional CSS', async ({ page }) => {
  const image = await page.evaluate(() => {
    const canvas = document.createElement('canvas');
    canvas.width = 80;
    canvas.height = 40;
    const context = canvas.getContext('2d')!;
    context.fillStyle = '#b02030';
    context.fillRect(0, 0, 80, 40);
    return canvas.toDataURL('image/png');
  });
  await page.route('**/api/visual-preview?**', (route) =>
    route.fulfill({
      contentType: 'application/x-ndjson',
      body: `${JSON.stringify({
        type: 'complete',
        html: `<html><head><style>p{color:rgb(0,128,0)}@media(min-width:500px){p{color:rgb(255,0,0)}}</style></head><body><p>Styled capture</p><img alt="Stored full-size image" src="${image}"></body></html>`,
      })}\n`,
    }),
  );
  await page.goto(previewPath());
  await page.getByRole('button', { name: 'Visual archive', exact: true }).click();
  const frame = page.frameLocator('iframe[title="Visual archive"]');
  const storedImage = frame.getByAltText('Stored full-size image');
  await expect(storedImage).toBeVisible();
  await expect
    .poll(() =>
      storedImage.evaluate((element: HTMLImageElement) => [
        element.naturalWidth,
        element.naturalHeight,
      ]),
    )
    .toEqual([80, 40]);
  const dimensions = await page.getByTitle('Visual archive', { exact: true }).boundingBox();
  await expect(frame.getByText('Styled capture')).toHaveCSS(
    'color',
    dimensions!.width >= 500 ? 'rgb(255, 0, 0)' : 'rgb(0, 128, 0)',
  );
});

test('an interrupted preparation does not leave a permanent spinner', async ({ page }) => {
  await page.route('**/api/visual-preview?**', (route) =>
    route.fulfill({
      contentType: 'application/x-ndjson',
      body: '{"type":"progress","completed":1,"total":2,"failed":0}\n',
    }),
  );
  await page.goto(previewPath());
  await page.getByRole('button', { name: 'Visual archive', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText(
    'The visual archive response was interrupted.',
  );
  await expect(page.getByTitle('Visual archive', { exact: true })).toHaveCount(0);
});

test('open document information leaves the archive beside it at the md breakpoint', async ({
  page,
}) => {
  await page.route('**/api/preview?**', (route) =>
    route.fulfill({
      json: {
        ...preview,
        title: 'A long article title that must remain readable in the information column',
        visual_available: true,
        details: {
          type: 'web',
          language: 'en',
          label: '',
          visits: 1,
          user_id: 0,
          metadata: Object.fromEntries(
            Array.from({ length: 30 }, (_, index) => [`property_${index}`, 'Stored metadata']),
          ),
        },
      },
    }),
  );
  await page.route('**/api/visual-preview?**', (route) =>
    route.fulfill({
      contentType: 'application/x-ndjson',
      body: '{"type":"complete","html":"<p>Captured appearance</p>"}\n',
    }),
  );
  await page.goto(previewPath());
  await page.getByRole('button', { name: 'Show document details', exact: true }).click();
  await page.getByRole('button', { name: 'Visual archive', exact: true }).click();
  const frame = page.getByTitle('Visual archive', { exact: true });
  await expect(frame).toBeVisible();

  for (const width of [375, 767, 768, 769, 1280, 2048]) {
    await page.setViewportSize({ width, height: 900 });
    await expect
      .poll(async () => {
        const info = await page.locator('.preview-header').boundingBox();
        const body = await page.locator('.preview-body').boundingBox();
        if (!info || !body) return false;
        return width >= 768
          ? info.width <= width * 0.25 + 1 &&
              body.x >= info.x + info.width - 1 &&
              Math.abs(body.y - info.y) < 100 &&
              body.width > info.width
          : body.y >= info.y + info.height && Math.abs(body.x - info.x) < 1;
      })
      .toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
  }

  await page.screenshot({ path: test.info().outputPath('information-beside-archive.png') });
  const beforeScroll = await frame.boundingBox();
  await page.locator('.preview-header').evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect(page.locator('#preview-document-details').getByText('property_29')).toBeVisible();
  expect(await frame.boundingBox()).toEqual(beforeScroll);
  await page.locator('.preview-header').evaluate((element) => {
    element.scrollTop = 0;
  });
  await page.getByRole('button', { name: 'Hide document details', exact: true }).click();
  await expect(page.locator('#preview-document-details')).toHaveCount(0);
  await expect
    .poll(async () => {
      const header = await page.locator('.preview-header').boundingBox();
      const body = await page.locator('.preview-body').boundingBox();
      return !!header && !!body && body.y >= header.y + header.height && body.width > 1800;
    })
    .toBe(true);
  await page.getByRole('button', { name: 'Readable', exact: true }).click();
  await expect(page.getByText('Saved article content')).toBeVisible();
  await page.getByRole('button', { name: 'Show document details', exact: true }).click();
  const info = await page.locator('.preview-header').boundingBox();
  const article = await page.locator('.preview-content').boundingBox();
  expect(article!.x).toBeGreaterThanOrEqual(info!.x + info!.width);
});
