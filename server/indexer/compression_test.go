package indexer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoo/hister/config"
	"github.com/asciimoo/hister/server/compression"
)

func TestHTMLCompressionAndExplicitMigration(t *testing.T) {
	for _, first := range compression.Algorithms {
		t.Run(first, func(t *testing.T) {
			store := newDataStore(t.TempDir())
			store.algorithm = first
			data := []byte(strings.Repeat("<p>Page content</p>", 100))
			key, err := store.write(htmlSubdir, data)
			if err != nil {
				t.Fatal(err)
			}
			original := strings.TrimSuffix(dataFilePath(store.dir, htmlSubdir, key), ".gz") + compression.Extension(first)
			before, err := os.Stat(original)
			if err != nil {
				t.Fatal(err)
			}
			second := compression.Gzip
			if first == second {
				second = compression.Zstd
			}
			store.algorithm = second
			if _, err := store.write(htmlSubdir, data); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(original)
			if err != nil || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("existing HTML was implicitly rewritten: %v", err)
			}
			decoded, err := store.read(htmlSubdir, key)
			if err != nil || !bytes.Equal(decoded, data) {
				t.Fatalf("mixed algorithm read failed: %v", err)
			}
			count, err := store.recompress(context.Background(), second)
			if err != nil || count != 1 {
				t.Fatalf("migration count=%d err=%v", count, err)
			}
			if _, err := os.Stat(original); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old encoding still exists: %v", err)
			}
			decoded, err = store.read(htmlSubdir, key)
			if err != nil || !bytes.Equal(decoded, data) {
				t.Fatalf("migration changed HTML: %v", err)
			}
			count, err = store.recompress(context.Background(), second)
			if err != nil || count != 0 {
				t.Fatalf("migration is not idempotent: count=%d err=%v", count, err)
			}
			store.deleteIfOrphaned("html_key", htmlSubdir, key, func(string, string) uint64 { return 0 })
			if _, err := store.read(htmlSubdir, key); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("orphan encoding was not deleted: %v", err)
			}
		})
	}
}

func TestHistoricalCompressionFailurePreservesSource(t *testing.T) {
	for _, failure := range []string{"cancel", "corrupt", "target"} {
		t.Run(failure, func(t *testing.T) {
			store := newDataStore(t.TempDir())
			data := []byte("<p>Historical HTML</p>")
			key, err := store.write(htmlSubdir, data)
			if err != nil {
				t.Fatal(err)
			}
			original := dataFilePath(store.dir, htmlSubdir, key)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "cancel":
				cancel()
			case "corrupt":
				if err := os.WriteFile(original, []byte("bad gzip"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "target":
				if err := os.Mkdir(zstdDataFilePath(store.dir, htmlSubdir, key), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.recompress(ctx, compression.Zstd); err == nil {
				t.Fatal("migration unexpectedly succeeded")
			}
			after, err := os.ReadFile(original)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed migration changed the source: %v", err)
			}
			pending, err := filepath.Glob(filepath.Join(filepath.Dir(original), ".pending-*"))
			if err != nil || len(pending) != 0 {
				t.Fatalf("temporary files remain: %v %v", pending, err)
			}
		})
	}
}

func TestHTMLCompressionLeavesFaviconsInGzip(t *testing.T) {
	store := newDataStore(t.TempDir())
	store.algorithm = compression.Zstd
	data := []byte("favicon bytes")
	key, err := store.write(faviconSubdir, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataFilePath(store.dir, faviconSubdir, key)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.recompress(context.Background(), compression.Zstd); err != nil {
		t.Fatal(err)
	}
	decoded, err := store.read(faviconSubdir, key)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatalf("favicon changed: %v", err)
	}
}

func TestConfiguredHistoricalCompression(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.CreateDefaultConfig()
		cfg.App.Directory = t.TempDir()
		store := newDataStore(filepath.Join(cfg.App.Directory, dataDirName))
		key, err := store.write(htmlSubdir, []byte("<p>Before startup</p>"))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Compression.Algorithm = compression.Zstd
		cfg.Compression.RecompressExisting = enabled
		idx, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		idx.Close()
		_, gzipErr := os.Stat(dataFilePath(store.dir, htmlSubdir, key))
		_, zstdErr := os.Stat(zstdDataFilePath(store.dir, htmlSubdir, key))
		if enabled && (!errors.Is(gzipErr, os.ErrNotExist) || zstdErr != nil) {
			t.Fatalf("enabled migration: gzip=%v zstd=%v", gzipErr, zstdErr)
		}
		if !enabled && (gzipErr != nil || !errors.Is(zstdErr, os.ErrNotExist)) {
			t.Fatalf("disabled migration rewrote history: gzip=%v zstd=%v", gzipErr, zstdErr)
		}
	}
}

func TestHTMLCompressionCleanup(t *testing.T) {
	store := newDataStore(t.TempDir())
	for _, algorithm := range compression.Algorithms {
		store.algorithm = algorithm
		if _, err := store.write(htmlSubdir, []byte("<p>"+algorithm+"</p>")); err != nil {
			t.Fatal(err)
		}
	}
	count, err := store.cleanup("html_key", htmlSubdir, func(string, string) uint64 { return 1 })
	if err != nil || count != 0 {
		t.Fatalf("referenced HTML was deleted: count=%d err=%v", count, err)
	}
	count, err = store.cleanup("html_key", htmlSubdir, func(string, string) uint64 { return 0 })
	if err != nil || count != 2 {
		t.Fatalf("mixed encoding cleanup: count=%d err=%v", count, err)
	}
}

func TestCompressionSettingSurvivesReindex(t *testing.T) {
	cfg := config.CreateDefaultConfig()
	cfg.App.Directory = t.TempDir()
	cfg.Compression.Algorithm = compression.Zstd
	idx, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.Reindex(nil, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	key, err := idx.data.write(htmlSubdir, []byte("<p>After reindex</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(zstdDataFilePath(idx.data.dir, htmlSubdir, key)); err != nil {
		t.Fatal("reindex lost the compression setting", err)
	}
}
