---
date: '2026-10-10T00:00:00+00:00'
draft: false
title: 'Visual Archive'
description: 'Save optional rendered page snapshots with CSS and shared resources, without changing search.'
---

The browser extension can save a separate visual snapshot of a page after JavaScript renders it. Search continues to use the normal document text and HTML. Visual HTML, stylesheets, images, and fonts do not enter the search index.

## Installation and Site Settings

Open **Rules → Visual archive** to select the installation mode:

| Mode       | Behavior                                                                                      |
| ---------- | --------------------------------------------------------------------------------------------- |
| `disabled` | Stop visual capture and visual replay for all sites. Site settings cannot override this mode. |
| `opt-in`   | Capture only sites with an enabled site setting.                                              |
| `opt-out`  | Capture all eligible sites except sites with a disabled site setting. This is the default.    |

Each site can inherit the installation mode, enable capture, or disable capture. Enter a hostname, not a URL or a wildcard. A hostname setting includes its subdomains. The most specific matching hostname wins. Normal allow, skip, sensitive-content, and access rules still apply. Manual indexing does not override visual archive exclusions.

Only an administrator can change the installation mode when user handling is enabled. Site settings belong to the current user. Installation changes from the Rules page are saved in `visual-settings.json` under `app.directory` and take effect without a restart. Site settings are saved with the other indexing rules.

The server configuration supports these settings:

```yaml
visual_archive:
  mode: opt-out
  avif_encoder: avifenc
  avif_decoder: avifdec
```

Set `visual_archive.mode: disabled` in the configuration file for a hard installation disable. The web interface cannot override it. Restart the server after editing the configuration file. `app.disable_previews: true` also disables visual capture and replay.

Use the matching new server and browser extension builds. An extension connected to a server without visual settings does not capture visual snapshots. Existing pages are not backfilled. Revisit or manually index an eligible page to create its snapshot.

## Capture and Storage

Capture saves the current DOM and available CSS rules, including runtime and adopted stylesheets. It keeps media queries, import conditions, and stylesheet media attributes. Unreadable cross-origin stylesheets are downloaded when possible. Referenced images, backgrounds, and fonts use a content-addressed resource store. Identical stored bytes share one file across pages.

Fonts share the same resource store across all sites, even when their source URLs differ. Capture accepts font files served with generic binary content types. Archives captured before this support was added need another capture to save their missing fonts.

The visual archive is stored in `data/visual/`, separately from `data/html/` and the search index. Each document has one latest visual snapshot. A new visual capture replaces the previous snapshot. A submission without a visual capture leaves the previous snapshot in place. The display shows its capture time. Historical text/HTML versions do not have visual snapshots.

New normal HTML previews, visual HTML, and CSS use Zstandard. Existing gzip previews remain readable and are not converted. The portable Go encoder selects the profile nearest to Zstandard level 9; its output is not identical to the native `zstd -9` benchmark. Images and fonts do not receive another outer compression layer.

With `avifenc` and `avifdec` installed, supported still raster images use this policy:

| Image class                               | Stored representation                   |
| ----------------------------------------- | --------------------------------------- |
| Screenshot-like image                     | Full resolution, AVIF quality 80, 4:4:4 |
| Other image under 100,000 source bytes    | Full resolution, AVIF quality 60        |
| Other image at least 100,000 source bytes | Half the total pixels, AVIF quality 60  |

Half the total pixels means approximately 70.7% of each original dimension, not half of each dimension. Screenshot detection uses a small pixel sample. It does not use OCR or an AI model, and can misclassify images. Quality values do not establish equal visual quality across formats.

The server keeps the original when conversion fails, codecs are unavailable, or AVIF is not smaller. SVG and animated images are not recompressed. Unsafe or unreadable raster dimensions are omitted. AVIF validation requires the decoder. The normal searchable document is retained if visual processing fails.

Capture limits are 8 MiB of HTML, 256 resources, 8 MiB per resource, and 24 MiB of decoded capture data. Raster processing limits images to 24 million pixels and 16,384 pixels per dimension. Downloads and codec processes have deadlines. A rejected oversized visual upload is retried once without the visual payload, so normal indexing can continue.

Document deletion removes its visual snapshot and resources that no other snapshot references. `hister cleanup` also removes orphaned visual files. Disabling capture does not delete existing files. Include `data/visual/` and `visual-settings.json` in a complete data-directory backup. Normal document export does not include the visual sidecar.

## Display and Network Access

Open a result preview and select **Visual archive**. The normal readable preview remains available. A progress indicator appears while the server prepares the snapshot. Closing or switching the preview cancels preparation and removes the displayed HTML from the component.

For each HTTP or HTTPS image, the server first tries the original URL. It uses a successful, validated response directly as an inline data URL. The browser does not download that image a second time. If the original is unavailable, the server uses the stored image. A reduced-resolution stored image is decoded and resized to its original intrinsic dimensions before display. This preserves intrinsic layout, but does not recover lost image detail.

Live image requests do not use the browser cookie jar or forward browser credentials. Private, loopback, and reserved network addresses are blocked, including redirect destinations. Authenticated or private-network originals can therefore require the stored fallback. Opening a visual preview can contact the original public image hosts from the server.

Replay uses a sandboxed iframe and a restrictive content security policy. Scripts, forms, nested frames, and external resource requests are disabled. HTTP and HTTPS links remain active and open the original destination in a separate tab. Fragment links stay inside the archive. Unsafe link schemes are removed. Resources are inline, not persistent browser blob URLs. They use browser memory while displayed, and become eligible for release when the iframe is removed. The browser controls the exact release time.

Replay sets the CSS opacity of picture containers with available inline images to 1. This replaces common JavaScript-controlled image fade-ins, which cannot finish in a scriptless archive. Explicit inline opacity, display, and visibility settings remain active. Other stylesheet rules can override this fallback when they have greater specificity.

## Limits of Appearance Preservation

Source CSS does not freeze every computed style. Another viewport, color scheme, browser, font availability, or CSS feature can change the result. CSS conditions remain active by design. Missing resources can also change the appearance.

The archive is a static document, not a working copy of the site. Shadow DOM, canvas pixels, video playback, scripts, and interactive state are not preserved. Current form values are removed from the visual copy. This is not a complete secret-removal system; use exclusions for private sites.
