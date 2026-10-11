package server

import (
	"bytes"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/asciimoo/hister/server/document"
	"github.com/asciimoo/hister/server/testutil"
	"github.com/asciimoo/hister/server/visual"
)

func TestVisualPreviewAuthorizationAndPolicy(t *testing.T) {
	cfg := testutil.Config(t)
	testutil.InitModelWithConfig(t, cfg)
	idx := newServerTestIndexer(t, cfg)
	cfg.VisualArchive.Mode = "opt-out"
	doc := &document.Document{URL: "https://example.com/visual", Title: "Readable title", Text: "Readable body", HTML: "<p>Readable body</p>", Processed: true, UserID: 2,
		Visual: &visual.Capture{HTML: `<head><link rel="stylesheet" href="/style.css" media="print"></head><body><p>Visual body</p></body>`, Resources: []visual.Asset{{URL: "https://example.com/style.css", Kind: "css", MIME: "text/css", Data: []byte(`p{color:red}`)}}},
	}
	if err := idx.AddDocument(doc); err != nil {
		t.Fatal(err)
	}
	cfg.App.UserHandling = true
	for _, scenario := range []struct {
		name    string
		user    uint
		mode    string
		version string
		status  int
	}{
		{"owner", 2, "opt-out", "", http.StatusOK},
		{"other owner", 1, "opt-out", "", http.StatusNotFound},
		{"anonymous", 0, "opt-out", "", http.StatusNotFound},
		{"disabled", 2, "disabled", "", http.StatusForbidden},
		{"historical", 2, "opt-out", "1", http.StatusNotFound},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cfg.VisualArchive.Mode = scenario.mode
			params := url.Values{"url": {doc.URL}, "document_id": {doc.ID()}}
			if scenario.version != "" {
				params.Set("version", scenario.version)
			}
			recorder := httptest.NewRecorder()
			serveVisualPreview(&webContext{Config: cfg, Indexer: idx, Request: httptest.NewRequest(http.MethodGet, "/api/visual-preview?"+params.Encode(), nil), Response: recorder, UserID: scenario.user})
			if recorder.Code != scenario.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if scenario.status != http.StatusOK {
				return
			}
			decoder := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes()))
			var event struct {
				Type       string `json:"type"`
				HTML       string `json:"html"`
				CapturedAt string `json:"captured_at"`
			}
			for decoder.More() {
				if err := decoder.Decode(&event); err != nil {
					t.Fatal(err)
				}
				if event.Type == "complete" {
					break
				}
			}
			if event.Type != "complete" || event.CapturedAt == "" || !strings.Contains(event.HTML, `media="print"`) || !strings.Contains(event.HTML, "p{color:red}") || !strings.Contains(html.UnescapeString(event.HTML), visual.ReplayCSP) {
				t.Fatalf("invalid replay: %+v", event)
			}
		})
	}
	params := url.Values{"url": {"https://different.example/visual"}, "document_id": {doc.ID()}}
	recorder := httptest.NewRecorder()
	serveVisualPreview(&webContext{Config: cfg, Indexer: idx, Request: httptest.NewRequest(http.MethodGet, "/api/visual-preview?"+params.Encode(), nil), Response: recorder, UserID: doc.UserID})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("mismatched URL accepted: %d", recorder.Code)
	}
}

func TestVisualPreviewTokenProtection(t *testing.T) {
	_, handler := newTokenTestServer(t, false)
	response := testutil.ServeHTTP(t, handler, http.MethodGet, "/api/visual-preview?url=https%3A%2F%2Fexample.com", nil, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
