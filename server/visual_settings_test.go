package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/asciimoo/hister/config"
	"github.com/asciimoo/hister/server/document"
	"github.com/asciimoo/hister/server/testutil"
	"github.com/asciimoo/hister/server/visual"
)

func TestVisualSettingsAPI(t *testing.T) {
	cfg, handler := newTokenTestServer(t, false)
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "hister://", "X-Access-Token": "secret"}
	for _, test := range []struct {
		form   url.Values
		status int
	}{
		{url.Values{"visual_mode": {"opt-in"}, "visual_sites": {`{"Example.COM":true}`}}, http.StatusOK},
		{url.Values{"skip": {"/private/"}}, http.StatusOK},
		{url.Values{"visual_mode": {"invalid"}}, http.StatusBadRequest},
		{url.Values{"visual_sites": {`{"https://example.com":true}`}}, http.StatusBadRequest},
		{url.Values{"visual_sites": {`{"example.com":"enabled"}`}}, http.StatusBadRequest},
		{url.Values{"visual_sites": {`{"example.com":false}`}, "skip": {"["}}, http.StatusBadRequest},
	} {
		rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/rules", strings.NewReader(test.form.Encode()), headers)
		if rec.Code != test.status {
			t.Fatalf("form=%v: status=%d body=%s", test.form, rec.Code, rec.Body.String())
		}
	}
	if cfg.VisualMode() != "opt-in" || !cfg.VisualEnabled("https://www.example.com/public", nil) || cfg.VisualEnabled("https://example.com/private/page", nil) {
		t.Fatal("visual policy was not preserved or indexing exclusions were bypassed")
	}
	rec := testutil.ServeHTTP(t, handler, http.MethodGet, "/api/rules", nil, headers)
	var response struct {
		Mode         string          `json:"visual_mode"`
		Sites        map[string]bool `json:"visual_sites"`
		CanConfigure bool            `json:"visual_can_configure_install"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Mode != "opt-in" || !response.Sites["example.com"] || !response.CanConfigure {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	cfg.VisualArchive.Mode = "disabled"
	rec = testutil.ServeHTTP(t, handler, http.MethodPost, "/api/rules", strings.NewReader(url.Values{"visual_mode": {"opt-out"}}.Encode()), headers)
	if rec.Code != http.StatusForbidden || cfg.VisualMode() != "disabled" {
		t.Fatal("API bypassed installation hard disable")
	}
}

func TestVisualInstallationAuthorization(t *testing.T) {
	cfg := testutil.Config(t)
	cfg.App.UserHandling = true
	context := &webContext{Config: cfg, UserID: 1}
	rules := &config.Rules{}
	if _, status, err := parseVisualSettings(context, url.Values{"visual_mode": {"opt-out"}}, rules); status != http.StatusForbidden || err == nil {
		t.Fatal("nonadministrator changed installation mode")
	}
	if _, status, err := parseVisualSettings(context, url.Values{"visual_sites": {`{"example.com":true}`}}, rules); status != 0 || err != nil || !rules.VisualSites["example.com"] {
		t.Fatal("user could not configure own sites")
	}
	context.IsAdmin = true
	if _, status, err := parseVisualSettings(context, url.Values{"visual_mode": {"opt-in"}}, rules); status != 0 || err != nil {
		t.Fatal("administrator could not configure installation")
	}
}

func TestVisualPatternRulesAPI(t *testing.T) {
	cfg, handler := newTokenTestServer(t, false)
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "hister://", "X-Access-Token": "secret"}
	form := url.Values{"visual_mode": {"opt-in"}, "visual_rules": {`{"(?i)^https://example\\.com/":"opt-in","/private":"opt-out"}`}}
	rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/rules", strings.NewReader(form.Encode()), headers)
	if rec.Code != http.StatusOK || !cfg.VisualEnabled("https://EXAMPLE.COM/public", nil) || cfg.VisualEnabled("https://example.com/private", nil) {
		t.Fatalf("visual-only rules failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cfg.Rules.Count() != 0 {
		t.Fatal("visual-only rules changed indexing rules")
	}
	for _, invalid := range []url.Values{
		{"visual_rules": {`{"example":"opt-out"}`}, "skip": {"["}},
		{"visual_rules": {`{"[":"opt-in"}`}},
		{"visual_rules": {`{"example":"--"}`}},
	} {
		rec = testutil.ServeHTTP(t, handler, http.MethodPost, "/api/rules", strings.NewReader(invalid.Encode()), headers)
		if rec.Code != http.StatusBadRequest || len(cfg.Rules.VisualRules) != 2 || !cfg.VisualEnabled("https://example.com/public", nil) {
			t.Fatalf("invalid rule save changed policy: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	rec = testutil.ServeHTTP(t, handler, http.MethodPost, "/api/rules", strings.NewReader(url.Values{"priority": {"example"}}.Encode()), headers)
	if rec.Code != http.StatusOK || len(cfg.Rules.VisualRules) != 2 {
		t.Fatal("normal rule save discarded visual rules")
	}
	for _, test := range []struct {
		url  string
		want bool
	}{
		{"https://EXAMPLE.COM/public", true}, {"https://example.com/private", false}, {"https://other.test", false},
	} {
		body, err := json.Marshal(map[string]any{"visual_policy": true, "url": test.url})
		if err != nil {
			t.Fatal(err)
		}
		rec = testutil.ServeHTTP(t, handler, http.MethodPost, "/api/rules/preview", strings.NewReader(string(body)), map[string]string{"Content-Type": "application/json", "Origin": "hister://", "X-Access-Token": "secret"})
		var response struct {
			Enabled *bool `json:"visual_enabled"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.Enabled == nil || *response.Enabled != test.want {
			t.Fatalf("policy check %s: status=%d body=%s err=%v", test.url, rec.Code, rec.Body.String(), err)
		}
	}
	cfg.VisualArchive.Mode = "disabled"
	if cfg.VisualEnabled("https://example.com/public", nil) {
		t.Fatal("saved opt-in bypassed installation disable")
	}
}

func TestExtensionVisualPolicyRequest(t *testing.T) {
	cfg, handler := newTokenTestServer(t, false)
	cfg.VisualArchive.Mode = "opt-in"
	cfg.Rules.VisualRules = map[string]string{"example.com": "opt-in", "/private": "opt-out"}
	if err := cfg.Rules.Compile(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{chromeExtensionOrigin, "moz-extension://test", "safari-web-extension://test", "https://evil.example"} {
		for _, pageURL := range []string{"https://example.com/public", "https://example.com/private"} {
			headers := map[string]string{"Origin": origin, "Content-Type": "application/json", "X-Access-Token": "secret"}
			body, err := json.Marshal(map[string]any{"visual_policy": true, "url": pageURL})
			if err != nil {
				t.Fatal(err)
			}
			rec := testutil.ServeHTTP(t, handler, http.MethodPost, "/api/rules/preview", strings.NewReader(string(body)), headers)
			if origin == "https://evil.example" {
				if rec.Code != http.StatusForbidden {
					t.Fatalf("untrusted origin status=%d, want 403", rec.Code)
				}
				continue
			}
			var response struct {
				Enabled *bool `json:"visual_enabled"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.Enabled == nil || *response.Enabled != strings.HasSuffix(pageURL, "/public") {
				t.Fatalf("origin=%s url=%s status=%d body=%s err=%v", origin, pageURL, rec.Code, rec.Body.String(), err)
			}
			preflight := testutil.ServeHTTP(t, handler, http.MethodOptions, "/api/rules/preview", nil, map[string]string{
				"Origin": origin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type,x-access-token",
			})
			if preflight.Code != http.StatusNoContent || preflight.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatalf("origin=%s preflight status=%d headers=%v", origin, preflight.Code, preflight.Header())
			}
		}
	}
}

func TestVisualSubmissionPolicy(t *testing.T) {
	for _, test := range []struct {
		name            string
		mode            string
		disablePreviews bool
		sites           map[string]bool
		skip            bool
		want            bool
	}{
		{"opt-out", "opt-out", false, nil, false, true},
		{"disabled", "disabled", false, map[string]bool{"example.com": true}, false, false},
		{"opt-in default", "opt-in", false, nil, false, false},
		{"opt-in enabled", "opt-in", false, map[string]bool{"example.com": true}, false, true},
		{"opt-out excluded", "opt-out", false, map[string]bool{"example.com": false}, false, false},
		{"previews disabled", "opt-out", true, nil, false, false},
		{"manual skip override", "opt-out", false, nil, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testutil.Config(t)
			cfg.VisualArchive.Mode = test.mode
			cfg.App.DisablePreviews = test.disablePreviews
			cfg.Rules = &config.Rules{VisualSites: test.sites, Skip: &config.Rule{}}
			if test.skip {
				cfg.Rules.Skip.ReStrs = []string{"example.com"}
				_ = cfg.Rules.Compile()
			}
			doc := &document.Document{URL: "https://example.com/page", Processed: true, DocumentID: "untrusted-id", Visual: &visual.Capture{HTML: "<p>hello</p>"}}
			doc.SetIgnoreSkipRules(true)
			context := &webContext{Config: cfg, Request: httptest.NewRequest(http.MethodPost, "/api/add", nil)}
			if status, err := prepareDocumentSubmission(context, doc); status != 0 || err != nil {
				t.Fatalf("status=%d err=%v", status, err)
			}
			if (doc.Visual != nil) != test.want {
				t.Fatalf("visual retained=%v, want %v", doc.Visual != nil, test.want)
			}
			if doc.ID() != document.GetDocID(doc.UserID, doc.URL) {
				t.Fatal("submitted document ID affected the authoritative ID")
			}
		})
	}
}

func TestProcessedVisualSubmissionChecksSensitiveData(t *testing.T) {
	cfg := testutil.Config(t)
	cfg.SensitiveContentPatterns = map[string]string{"test": "secret-value", "visual": "visual-marker"}
	context := &webContext{Config: cfg, Request: httptest.NewRequest(http.MethodPost, "/api/add", nil)}
	for _, capture := range []*visual.Capture{
		{HTML: "<p>secret-value</p>"},
		{Resources: []visual.Asset{{Kind: "css", Data: []byte("secret-value")}}},
	} {
		doc := &document.Document{URL: "https://example.com/page", Processed: true, Visual: capture}
		if status, err := prepareDocumentSubmission(context, doc); status != 0 || err != nil || doc.Visual != nil {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
	for _, doc := range []*document.Document{
		{URL: "https://example.com/page", Processed: true, HTML: "secret-value", Visual: &visual.Capture{HTML: "<p>safe</p>"}},
		{URL: "https://example.com/page", Processed: true, Text: "secret-value", Visual: &visual.Capture{HTML: "<p>safe</p>"}},
		{URL: "https://example.com/page", Processed: true, HTML: "secret-value", Visual: &visual.Capture{HTML: "visual-marker"}},
	} {
		if status, err := prepareDocumentSubmission(context, doc); status != http.StatusUnprocessableEntity || err != document.ErrSensitiveContent {
			t.Fatalf("sensitive document accepted: status=%d err=%v", status, err)
		}
	}
}
