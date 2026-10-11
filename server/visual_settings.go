package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"

	"github.com/asciimoo/hister/config"
	"github.com/asciimoo/hister/server/document"
)

func canConfigureVisualInstall(c *webContext) bool {
	return canWrite(c) && (!c.Config.App.UserHandling || c.IsAdmin) && !c.Config.App.DisablePreviews && c.Config.VisualArchive.Mode != "disabled"
}

func parseVisualSettings(c *webContext, form url.Values, rules *config.Rules) (string, int, error) {
	mode := form.Get("visual_mode")
	if form.Has("visual_mode") {
		if !canConfigureVisualInstall(c) {
			return "", http.StatusForbidden, errors.New("installation settings require administrator access and an enabled installation configuration")
		}
		if mode == "" {
			return "", http.StatusBadRequest, errors.New("visual archive mode is required")
		}
		if err := config.ValidateVisualMode(mode); err != nil {
			return "", http.StatusBadRequest, err
		}
	}
	if form.Has("visual_sites") {
		var sites map[string]bool
		if err := json.Unmarshal([]byte(form.Get("visual_sites")), &sites); err != nil {
			return "", http.StatusBadRequest, err
		}
		normalized, err := config.NormalizeVisualSites(sites)
		if err != nil {
			return "", http.StatusBadRequest, err
		}
		rules.VisualSites = normalized
	}
	return mode, 0, nil
}

func validateVisualSubmission(c *webContext, doc *document.Document) (int, error) {
	if doc.Visual == nil {
		return 0, nil
	}
	doc.DocumentID = document.GetDocID(doc.UserID, doc.URL)
	if !c.Config.VisualEnabled(doc.URL, c.effectiveRules()) {
		doc.Visual = nil
		return 0, nil
	}
	if doc.SkipSensitiveCheck {
		return 0, nil
	}
	for _, pattern := range c.Config.SensitiveContentPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		if compiled.MatchString(doc.HTML) || compiled.MatchString(doc.Text) || compiled.MatchString(doc.Visual.HTML) {
			return http.StatusUnprocessableEntity, document.ErrSensitiveContent
		}
		for _, resource := range doc.Visual.Resources {
			if resource.Kind == "css" && compiled.Match(resource.Data) {
				return http.StatusUnprocessableEntity, document.ErrSensitiveContent
			}
		}
	}
	return 0, nil
}
