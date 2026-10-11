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
