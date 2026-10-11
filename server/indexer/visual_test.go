package indexer

import (
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoo/hister/server/document"
	"github.com/asciimoo/hister/server/testutil"
	"github.com/asciimoo/hister/server/visual"
)

func visualDocument(url string) *document.Document {
	return &document.Document{URL: url, Title: "Searchable title", Text: "Searchable ordinary text", Processed: true,
		Visual: &visual.Capture{HTML: "<html><body>visualsecretword</body></html>", Resources: []visual.Asset{{URL: "https://example.com/a.css", MIME: "text/css", Kind: "css", Data: []byte(".visualsecretword{color:red}")}}},
	}
}

func TestVisualArchiveDoesNotAffectSearchAndIsDeleted(t *testing.T) {
	cfg := testutil.Config(t)
	idx := newTestIndexer(t, cfg)
	defer idx.Close()
	doc := visualDocument("https://example.com/visual")
	if err := idx.AddDocumentContext(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if doc.Visual != nil {
		t.Fatal("visual payload retained on indexed document")
	}
	if _, err := idx.ReadVisual(doc.ID()); err != nil {
		t.Fatal(err)
	}
	result, err := idx.Search(&Query{Text: "visualsecretword"})
	if err != nil || len(result.Documents) != 0 {
		t.Fatalf("visual content entered search: %#v, %v", result, err)
	}
	result, err = idx.Search(&Query{Text: "Searchable"})
	if err != nil || len(result.Documents) != 1 {
		t.Fatalf("search behavior changed: %#v, %v", result, err)
	}
	if err := idx.Delete(doc.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.ReadVisual(doc.ID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("visual capture retained after delete: %v", err)
	}
}

func TestVisualArchiveBatchDeleteAndDisabledPolicy(t *testing.T) {
	cfg := testutil.Config(t)
	idx := newTestIndexer(t, cfg)
	defer idx.Close()
	doc := visualDocument("https://example.com/batch-visual")
	batch := idx.NewMultiBatch()
	if err := batch.Add(doc); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.ReadVisual(doc.ID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("batch published a visual before commit", err)
	}
	if err := batch.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.ReadVisual(doc.ID()); err != nil {
		t.Fatal(err)
	}
	batch.Delete(doc.ID())
	if err := batch.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.ReadVisual(doc.ID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("batch deletion retained a visual", err)
	}
	if err := cfg.SetVisualMode("disabled"); err != nil {
		t.Fatal(err)
	}
	doc = visualDocument("https://example.com/disabled")
	if err := idx.Add(doc); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.ReadVisual(doc.ID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled policy stored a visual", err)
	}
}

func TestVisualArchiveSurvivesReindex(t *testing.T) {
	cfg := testutil.Config(t)
	idx := newTestIndexer(t, cfg)
	defer idx.Close()
	before := visualDocument("https://example.com/before-reindex")
	if err := idx.Add(before); err != nil {
		t.Fatal(err)
	}
	store := idx.visual
	if err := idx.Reindex(cfg.RulesSnapshot(), false, cfg.Indexer.DetectLanguages, cfg.Indexer.KeepStopwords, nil); err != nil {
		t.Fatal(err)
	}
	if idx.visual != store || idx.visualConfig != cfg {
		t.Fatal("reindex did not preserve visual storage and configuration")
	}
	if _, err := idx.ReadVisual(before.ID()); err != nil {
		t.Fatal("existing visual lost after reindex", err)
	}
	after := visualDocument("https://example.com/after-reindex")
	if err := idx.Add(after); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.ReadVisual(after.ID()); err != nil {
		t.Fatal("new visual not stored after reindex", err)
	}
}

func TestHTMLDatastoreZstdAndLegacyGzip(t *testing.T) {
	directory := t.TempDir()
	store := newDataStore(directory)
	data := []byte(strings.Repeat("<p>Page content</p>", 100))
	key, err := store.write(htmlSubdir, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(zstdDataFilePath(directory, htmlSubdir, key)); err != nil {
		t.Fatal("new HTML is not zstd", err)
	}
	decoded, err := store.read(htmlSubdir, key)
	if err != nil || string(decoded) != string(data) {
		t.Fatalf("zstd round trip: %v", err)
	}
	if err := os.Remove(zstdDataFilePath(directory, htmlSubdir, key)); err != nil {
		t.Fatal(err)
	}
	path := dataFilePath(directory, htmlSubdir, key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err = store.read(htmlSubdir, key)
	if err != nil || string(decoded) != string(data) {
		t.Fatalf("legacy gzip round trip: %v", err)
	}
	if _, err := store.write(htmlSubdir, data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("existing gzip was replaced", err)
	}
	store.deleteIfOrphaned("html_key", htmlSubdir, key, func(string, string) uint64 { return 0 })
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("gzip was not deleted", err)
	}
}
