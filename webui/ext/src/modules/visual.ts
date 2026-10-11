export type VisualAsset = {
  url: string;
  mime: string;
  kind: string;
  data: string;
  width?: number;
  height?: number;
};

export type VisualCapture = {
  html: string;
  viewport_width: number;
  viewport_height: number;
  color_scheme: string;
  resources: VisualAsset[];
};

export type VisualPolicy = {
  visual_mode?: string;
  visual_sites?: Record<string, boolean>;
};

const maxAssets = 256;
const maxAssetBytes = 8 * 1024 * 1024;
const maxTotalBytes = 24 * 1024 * 1024;
const encoder = new TextEncoder();

export function visualCaptureEnabled(url: string, policy: VisualPolicy): boolean {
  if (policy.visual_mode !== 'opt-in' && policy.visual_mode !== 'opt-out') return false;
  try {
    const parsed = new URL(url);
    if (['http:', 'https:'].indexOf(parsed.protocol) < 0) return false;
    let host = parsed.hostname.toLowerCase().replace(/\.$/, '');
    while (host) {
      if (Object.prototype.hasOwnProperty.call(policy.visual_sites ?? {}, host))
        return policy.visual_sites![host] === true;
      const dot = host.indexOf('.');
      if (dot < 0) break;
      host = host.slice(dot + 1);
    }
    return policy.visual_mode === 'opt-out';
  } catch {
    return false;
  }
}

function resourceURL(value: string, base: string): string | undefined {
  try {
    const parsed = new URL(value, base);
    if (['http:', 'https:', 'data:', 'blob:'].indexOf(parsed.protocol) < 0) return;
    if (parsed.username || parsed.password || value.trim().startsWith('#')) return;
    if (parsed.href.length > 32768 && ['data:', 'blob:'].indexOf(parsed.protocol) < 0) return;
    return parsed.href;
  } catch {
    return;
  }
}

export function resolveCSS(css: string, base: string): string {
  return css.replace(
    /url\(\s*(?:"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|([^)]*))\s*\)|@import\s+(["'])(.*?)\4/gi,
    (match, double, single, bare, quote, imported) => {
      const raw = (double ?? single ?? bare ?? imported ?? '').trim();
      const url = resourceURL(raw, base);
      if (!url) return match;
      const escaped = url.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
      return imported === undefined ? `url("${escaped}")` : `@import "${escaped}"`;
    },
  );
}

export function cssReferences(css: string, base: string): VisualAsset[] {
  const resources = new Map<string, VisualAsset>();
  const absolute = resolveCSS(css, base);
  const references = /url\("((?:\\.|[^"\\])*)"\)|@import\s+"(.*?)"/gi;
  let match: RegExpExecArray | null;
  while ((match = references.exec(absolute))) {
    const url = resourceURL(match[1] ?? match[2], base);
    if (!url) continue;
    const kind =
      match[2] || /\.css(?:[?#]|$)/i.test(url)
        ? 'css'
        : /\.(woff2?|ttf|otf)(?:[?#]|$)/i.test(url)
          ? 'font'
          : 'image';
    resources.set(url, { url, mime: '', kind, data: '' });
  }
  return [...resources.values()];
}

function base64(bytes: Uint8Array): string {
  let binary = '';
  for (let offset = 0; offset < bytes.length; offset += 32768) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 32768));
  }
  return btoa(binary);
}

function decodedBytes(data: string): number {
  return (data.length * 3) / 4 - (data.endsWith('==') ? 2 : data.endsWith('=') ? 1 : 0);
}

async function readAsset(
  asset: VisualAsset,
): Promise<{ bytes: Uint8Array; base: string } | undefined> {
  const response = await fetch(asset.url, {
    credentials: 'include',
    signal: AbortSignal.timeout(5000),
  });
  if (!response.ok || Number(response.headers.get('content-length')) > maxAssetBytes) return;
  const reader = response.body?.getReader();
  if (!reader) return;
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.length;
      if (length > maxAssetBytes) return;
      chunks.push(value);
    }
  } finally {
    await reader.cancel().catch(() => {});
  }
  asset.mime = (response.headers.get('content-type') ?? '').split(';')[0].toLowerCase();
  if (asset.mime === 'text/html' || asset.mime === 'application/xhtml+xml') return;
  if (asset.kind === 'css' || asset.mime === 'text/css') {
    asset.kind = 'css';
    asset.mime = 'text/css';
  } else if (asset.mime.startsWith('font/') || /font|woff|opentype/.test(asset.mime)) {
    asset.kind = 'font';
    if (!asset.mime.startsWith('font/')) {
      const extension = new URL(asset.url).pathname.match(/\.(woff2?|ttf|otf|eot)$/i)?.[1];
      const format =
        extension?.toLowerCase() ??
        (/woff2/.test(asset.mime)
          ? 'woff2'
          : /woff/.test(asset.mime)
            ? 'woff'
            : /opentype/.test(asset.mime)
              ? 'otf'
              : /truetype/.test(asset.mime)
                ? 'ttf'
                : undefined);
      if (!format) return;
      asset.mime = format === 'eot' ? 'application/vnd.ms-fontobject' : `font/${format}`;
    }
  } else if (asset.kind === 'font' && /^(application|binary)\/octet-stream$/.test(asset.mime)) {
    const extension = new URL(asset.url).pathname.match(/\.(woff2?|ttf|otf)$/i)?.[1];
    if (!extension) return;
    asset.mime = `font/${extension.toLowerCase()}`;
  } else if (!asset.mime.startsWith('image/')) {
    return;
  }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.length;
  }
  return { bytes, base: response.url || asset.url };
}

export async function completeVisualCapture(
  capture: VisualCapture,
  localOnly = false,
): Promise<VisualCapture> {
  const resources = new Map(capture.resources.map((asset) => [asset.url, asset]));
  let total = encoder.encode(capture.html ?? '').length;
  for (const asset of resources.values()) total += encoder.encode(asset.url).length;
  for (const asset of resources.values()) {
    const size = decodedBytes(asset.data);
    if (size > maxAssetBytes || total + size > maxTotalBytes) asset.data = '';
    else total += size;
  }
  const pending = [...resources.values()].filter(
    (asset) => !asset.data && (!localOnly || /^(data|blob):/.test(asset.url)),
  );
  const deadline = Date.now() + 20_000;
  const addReferences = (css: string, base: string) => {
    for (const asset of cssReferences(css, base)) {
      if (resources.size >= maxAssets || resources.has(asset.url)) continue;
      resources.set(asset.url, asset);
      total += encoder.encode(asset.url).length;
      if (!localOnly || /^(data|blob):/.test(asset.url)) pending.push(asset);
    }
  };
  for (const asset of resources.values()) {
    if (asset.kind === 'css' && asset.data) {
      addReferences(
        new TextDecoder().decode(
          Uint8Array.from(atob(asset.data), (character) => character.charCodeAt(0)),
        ),
        asset.url,
      );
    }
  }
  while (pending.length && total < maxTotalBytes && Date.now() < deadline) {
    await Promise.all(
      pending.splice(0, 6).map(async (asset) => {
        try {
          const result = await readAsset(asset);
          if (!result) return;
          let { bytes } = result;
          if (asset.kind === 'css') {
            const css = resolveCSS(new TextDecoder().decode(bytes), result.base);
            bytes = encoder.encode(css);
            if (bytes.length > maxAssetBytes) return;
            addReferences(css, result.base);
          }
          if (total + bytes.length > maxTotalBytes) return;
          total += bytes.length;
          asset.data = base64(bytes);
        } catch {
          return;
        }
      }),
    );
  }
  capture.resources = [...resources.values()].slice(0, maxAssets);
  return capture;
}

export function fitVisualCapture(
  document: { visual?: VisualCapture; [key: string]: unknown },
  maxBytes = 36 * 1024 * 1024,
): void {
  const capture = document.visual;
  if (!capture) return;
  let total = encoder.encode(capture.html).length;
  let size = encoder.encode(
    JSON.stringify({ ...document, visual: { ...capture, resources: [] } }),
  ).length;
  if (size > maxBytes || total > maxTotalBytes) {
    delete document.visual;
    return;
  }
  const priority = (asset: VisualAsset) =>
    asset.kind === 'css' ? 0 : asset.kind === 'font' ? 1 : 2;
  capture.resources = [...capture.resources]
    .sort((first, second) => priority(first) - priority(second))
    .filter((asset) => {
      const bytes = encoder.encode(JSON.stringify(asset)).length + 1;
      const decoded = decodedBytes(asset.data) + encoder.encode(asset.url).length;
      if (size + bytes > maxBytes || total + decoded > maxTotalBytes) return false;
      size += bytes;
      total += decoded;
      return true;
    });
}

export async function captureVisual(expectedURL: string): Promise<VisualCapture | undefined> {
  if (location.href !== expectedURL) return;
  const root = document.documentElement.cloneNode(true) as HTMLElement;
  const resources = new Map<string, VisualAsset>();
  const add = (value: string, kind: string, width?: number, height?: number) => {
    const url = resourceURL(value, document.baseURI);
    if (!url || resources.size >= maxAssets) return;
    const asset = resources.get(url) ?? { url, kind, mime: '', data: '' };
    if (width && height) Object.assign(asset, { width, height });
    resources.set(url, asset);
    return asset;
  };
  const addCSS = (css: string, base: string) => {
    for (const asset of cssReferences(css, base)) add(asset.url, asset.kind);
  };
  const originals = [...document.documentElement.querySelectorAll('*')];
  const copies = [...root.querySelectorAll('*')];
  for (let index = 0; index < originals.length; index++) {
    const original = originals[index];
    const copy = copies[index];
    if (original instanceof HTMLImageElement) {
      const asset = add(
        original.currentSrc || original.src,
        'image',
        original.naturalWidth,
        original.naturalHeight,
      );
      if (asset) copy.setAttribute('src', asset.url);
      copy.removeAttribute('srcset');
      copy.removeAttribute('sizes');
      copy.setAttribute('loading', 'eager');
    }
    if (original instanceof HTMLSourceElement && original.parentElement?.tagName === 'PICTURE') {
      copy.removeAttribute('srcset');
    }
    if (original.hasAttribute('style')) {
      const css = resolveCSS(original.getAttribute('style')!, document.baseURI);
      copy.setAttribute('style', css);
      addCSS(css, document.baseURI);
    }
    if (original instanceof HTMLInputElement) copy.removeAttribute('value');
    if (original instanceof HTMLTextAreaElement) copy.textContent = '';
  }
  for (const sheet of document.styleSheets) {
    const owner = sheet.ownerNode as Element | null;
    const copy = owner ? copies[originals.indexOf(owner)] : undefined;
    if (sheet.disabled) {
      copy?.remove();
      continue;
    }
    if (copy) {
      if (sheet.media.mediaText) copy.setAttribute('media', sheet.media.mediaText);
      else copy.removeAttribute('media');
    }
    const base = sheet.href ?? document.baseURI;
    try {
      const css = resolveCSS([...sheet.cssRules].map((rule) => rule.cssText).join('\n'), base);
      addCSS(css, base);
      if (sheet.href) {
        const asset = add(sheet.href, 'css');
        if (asset && encoder.encode(css).length <= maxAssetBytes) {
          asset.mime = 'text/css';
          asset.data = base64(encoder.encode(css));
        }
        copy?.setAttribute('href', sheet.href);
      } else if (copy) {
        copy.textContent = css;
      }
    } catch {
      if (sheet.href) {
        add(sheet.href, 'css');
        copy?.setAttribute('href', sheet.href);
      }
    }
  }
  for (const sheet of document.adoptedStyleSheets ?? []) {
    if (sheet.disabled) continue;
    const style = document.createElement('style');
    if (sheet.media.mediaText) style.media = sheet.media.mediaText;
    try {
      style.textContent = resolveCSS(
        [...sheet.cssRules].map((rule) => rule.cssText).join('\n'),
        document.baseURI,
      );
      addCSS(style.textContent, document.baseURI);
      root.querySelector('head')?.append(style);
    } catch {
      continue;
    }
  }
  root
    .querySelectorAll('script, noscript, iframe, object, embed, base')
    .forEach((node) => node.remove());
  if (location.href !== expectedURL) return;
  const capture = await completeVisualCapture(
    {
      html: '',
      viewport_width: innerWidth,
      viewport_height: innerHeight,
      color_scheme: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
      resources: [...resources.values()],
    },
    true,
  );
  const localURLs = new Map<string, string>();
  for (const asset of capture.resources) {
    if (/^(data|blob):/.test(asset.url) && asset.data) {
      const replacement = `blob:hister-visual/${localURLs.size}`;
      localURLs.set(asset.url, replacement);
      asset.url = replacement;
    }
  }
  const rewrite = (value: string) => {
    for (const [url, replacement] of localURLs) value = value.split(url).join(replacement);
    return value;
  };
  for (const node of root.querySelectorAll('*')) {
    for (const attribute of [...node.attributes]) {
      node.setAttribute(attribute.name, rewrite(attribute.value));
    }
    if (node.tagName === 'STYLE') node.textContent = rewrite(node.textContent ?? '');
  }
  for (const asset of capture.resources) {
    if (asset.kind === 'css' && asset.data) {
      const css = new TextDecoder().decode(
        Uint8Array.from(atob(asset.data), (character) => character.charCodeAt(0)),
      );
      asset.data = base64(encoder.encode(rewrite(css)));
    }
  }
  capture.html = '<!doctype html>' + root.outerHTML;
  if (encoder.encode(capture.html).length > maxAssetBytes) return;
  return location.href === expectedURL ? capture : undefined;
}
