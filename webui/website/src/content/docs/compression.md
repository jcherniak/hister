---
date: '2026-10-11T00:00:00+00:00'
draft: false
title: 'HTML Storage Compression'
description: 'Select gzip or Zstandard for HTML previews and optionally convert historical files.'
---

The configuration file selects the compression algorithm for new HTML previews:

```yaml
compression:
  algorithm: zstd
  recompress_existing: false
```

The supported algorithms are `gzip` and `zstd`. The default is `gzip`. Restart Hister after changing the configuration file. The Zstandard encoder uses the portable Go profile nearest to level 9. Its output is not identical to the native `zstd -9` command.

Hister reads both `.gz` and `.zst` files regardless of the selected algorithm. Changing the algorithm does not convert existing files. Identical HTML that is already stored retains its current encoding.

Set `recompress_existing: true` to convert historical HTML previews to the selected algorithm at startup. Make a backup first. Conversion delays startup and can use temporary disk space. Each file is decoded, checked against its content hash, written atomically, and verified before the old encoding is removed. A failure stops startup without deleting the source file. Completed conversions remain valid and the next run can continue. Set this option back to `false` after conversion to avoid another scan at every startup.

Compression does not change document IDs, search content, or search indexes. It does not recompress images, fonts, favicons, SQLite databases, or other stores. Restore gzip files before running an older Hister build that cannot read Zstandard.
