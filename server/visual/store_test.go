package visual

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoo/hister/server/compression"
)

func testCapture(css string) *Capture {
	return &Capture{HTML: "<html><head></head><body>Visual-only content</body></html>", ViewportWidth: 1200, ViewportHeight: 800, Resources: []Asset{
		{URL: "https://example.com/style.css", MIME: "text/css", Kind: "css", Data: []byte(css)},
		{URL: "https://example.com/font.woff2", MIME: "font/woff2", Kind: "font", Data: []byte("shared-font-bytes")},
	}}
}

func TestVisualStoreSharedResourcesAndRestart(t *testing.T) {
	directory := t.TempDir()
	store := NewStore(directory)
	for _, id := range []string{"first", "second"} {
		capture := testCapture("body{color:red}")
		capture.Resources[1].URL = "https://" + id + ".example/font.woff2"
		if err := store.Save(context.Background(), id, capture); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Read("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Read("second")
	if err != nil {
		t.Fatal(err)
	}
	if first.HTMLKey != second.HTMLKey || first.Resources[0].Key != second.Resources[0].Key || first.Resources[1].Key != second.Resources[1].Key {
		t.Fatal("shared content was not deduplicated")
	}
	css, err := store.ReadResource(first.Resources[0].Key, "css")
	if err != nil || string(css) != "body{color:red}" {
		t.Fatalf("CSS round trip: %q, %v", css, err)
	}
	fontPath := store.blobPath("assets", first.Resources[1].Key)
	font, err := os.ReadFile(fontPath)
	if err != nil || string(font) != "shared-font-bytes" {
		t.Fatalf("font payload was recompressed: %q, %v", font, err)
	}
	store = NewStore(directory)
	if err := store.Delete("first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadResource(second.Resources[0].Key, "css"); err != nil {
		t.Fatal("deletion removed a shared resource", err)
	}
	if _, err := store.ReadResource(second.Resources[1].Key, "font"); err != nil {
		t.Fatal("deletion removed a font used by another site", err)
	}
	if err := store.Save(context.Background(), "second", testCapture("body{color:blue}")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadResource(first.Resources[0].Key, "css"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced CSS retained: %v", err)
	}
	latest, err := store.Read("second")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("second"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadHTML(latest.HTMLKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced HTML retained: %v", err)
	}
	if _, err := os.Stat(fontPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced font retained: %v", err)
	}
}

func TestVisualStoreNormalizesResourceMIME(t *testing.T) {
	store := NewStore(t.TempDir())
	capture := testCapture("body{color:red}")
	capture.Resources[0].MIME = `text/css; charset="utf-8"`
	capture.Resources[1].MIME = `font/woff2; name="quoted\"font"`
	if err := store.Save(context.Background(), "page", capture); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Read("page")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Resources[0].MIME != "text/css" || manifest.Resources[1].MIME != "font/woff2" {
		t.Fatalf("unnormalized resources: %+v", manifest.Resources)
	}
}

func TestVisualStoreConcurrentWrites(t *testing.T) {
	store := NewStore(t.TempDir())
	var jobs sync.WaitGroup
	for index := range 12 {
		jobs.Go(func() {
			if err := store.Save(context.Background(), strings.Repeat("x", index+1), testCapture("body{color:red}")); err != nil {
				t.Error(err)
			}
		})
	}
	jobs.Wait()
	removed, err := store.Cleanup(func(id string) bool { return id == "x" })
	if err != nil || removed != 11 {
		t.Fatalf("cleanup: %d, %v", removed, err)
	}
	manifest, err := store.Read("x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadResource(manifest.Resources[0].Key, "css"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("x"); err != nil {
		t.Fatal(err)
	}
}

func TestVisualStoreValidationAndCancellation(t *testing.T) {
	store := NewStore(t.TempDir())
	capture := testCapture("body{color:red}")
	capture.Resources[0].URL = "file:///etc/passwd"
	if err := store.Save(context.Background(), "invalid", capture); err == nil {
		t.Fatal("file URL accepted")
	}
	capture = testCapture("body{color:red}")
	capture.Resources[0].Data = make([]byte, MaxResourceBytes+1)
	if err := store.Save(context.Background(), "invalid", capture); err == nil {
		t.Fatal("oversized asset accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(ctx, "canceled", testCapture("body{color:red}")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := store.Read("canceled"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled capture was published", err)
	}
	if _, err := store.ReadHTML("../outside"); err == nil {
		t.Fatal("invalid resource key accepted")
	}
}

func TestVisualTextCompressionLimit(t *testing.T) {
	data := []byte(strings.Repeat("<p>archived page</p>", 1000))
	compressed, err := compression.Compress(compression.Zstd, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) >= len(data) {
		t.Fatal("text was not compressed")
	}
	decoded, err := compression.Decompress(compression.Zstd, compressed, len(data))
	if err != nil || string(decoded) != string(data) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := compression.Decompress(compression.Zstd, compressed, len(data)-1); err == nil {
		t.Fatal("decompression size limit ignored")
	}
}

func TestVisualStoreDoesNotPublishDeletedDocument(t *testing.T) {
	store := NewStore(t.TempDir())
	checked := false
	err := store.SaveIfPresent(context.Background(), "deleted", testCapture("body{color:red}"), func(string) bool {
		checked = true
		return false
	})
	if err != nil || !checked {
		t.Fatalf("publication guard: checked=%v, error=%v", checked, err)
	}
	if _, err := store.Read("deleted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted document published", err)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("publication guard wrote files: %v, %v", entries, err)
	}
}

type blockedCaptureContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (ctx *blockedCaptureContext) Err() error {
	ctx.once.Do(func() { close(ctx.entered); <-ctx.release })
	return ctx.Context.Err()
}

func TestVisualStoreNewerCaptureWins(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := &blockedCaptureContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- store.Save(ctx, "page", &Capture{HTML: "<p>older</p>"}) }()
	<-ctx.entered
	if err := store.Save(context.Background(), "page", &Capture{HTML: "<p>newer</p>"}); err != nil {
		close(ctx.release)
		t.Fatal(err)
	}
	close(ctx.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Read("page")
	if err != nil {
		t.Fatal(err)
	}
	html, err := store.ReadHTML(manifest.HTMLKey)
	if err != nil || string(html) != "<p>newer</p>" {
		t.Fatalf("stale capture published: %s, %v", html, err)
	}
}
