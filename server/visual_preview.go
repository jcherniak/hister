package server

import (
	"encoding/json"
	"net/http"

	"github.com/asciimoo/hister/server/document"
	"github.com/asciimoo/hister/server/visual"
)

func serveVisualPreview(c *webContext) {
	pageURL := c.Request.URL.Query().Get("url")
	doc := requestedDocument(c, pageURL)
	if doc == nil || c.Request.URL.Query().Get("version") != "" {
		http.Error(c.Response, "visual archive not found", http.StatusNotFound)
		return
	}
	pageURL = doc.URL
	if !c.Config.VisualEnabled(pageURL, c.effectiveRules()) {
		http.Error(c.Response, "visual archives are disabled for this site", http.StatusForbidden)
		return
	}
	manifest, err := c.Indexer.ReadVisual(document.GetDocID(doc.UserID, doc.URL))
	if err != nil || manifest == nil {
		http.Error(c.Response, "visual archive not found", http.StatusNotFound)
		return
	}
	markup, err := c.Indexer.ReadVisualHTML(manifest.HTMLKey)
	if err != nil {
		serve500(c)
		return
	}
	c.Response.Header().Set("Content-Type", "application/x-ndjson")
	c.Response.Header().Set("Cache-Control", "no-store")
	c.Response.Header().Set("X-Content-Type-Options", "nosniff")
	encoder := json.NewEncoder(c.Response)
	write := func(value any) {
		if encoder.Encode(value) == nil {
			if flusher, ok := c.Response.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
	ctx := visual.WithCodecs(c.Request.Context(), c.Config.VisualArchive.AVIFEncoder, c.Config.VisualArchive.AVIFDecoder)
	markupString, err := visual.Render(ctx, pageURL, manifest, markup, func(resource visual.Resource) ([]byte, error) {
		return c.Indexer.ReadVisualResource(resource.Key, resource.Kind)
	}, func(completed, total, failed int) {
		write(map[string]any{"type": "progress", "completed": completed, "total": total, "failed": failed})
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		write(map[string]string{"type": "error", "error": "Could not prepare the visual archive."})
		return
	}
	write(map[string]any{"type": "complete", "html": markupString, "viewport_width": manifest.ViewportWidth, "viewport_height": manifest.ViewportHeight, "captured_at": manifest.CapturedAt})
}
