package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/net/idna"
)

type VisualArchive struct {
	Mode        string `yaml:"mode" mapstructure:"mode"`
	AVIFEncoder string `yaml:"avif_encoder" mapstructure:"avif_encoder"`
	AVIFDecoder string `yaml:"avif_decoder" mapstructure:"avif_decoder"`
}

func ValidateVisualMode(mode string) error {
	switch mode {
	case "", "disabled", "opt-in", "opt-out":
		return nil
	default:
		return errors.New("visual_archive.mode must be disabled, opt-in, or opt-out")
	}
}

func NormalizeVisualSites(sites map[string]bool) (map[string]bool, error) {
	if len(sites) > 1000 {
		return nil, errors.New("too many visual archive site settings")
	}
	normalized := make(map[string]bool, len(sites))
	for domain, enabled := range sites {
		host, err := visualHostname(domain)
		if err != nil {
			return nil, err
		}
		if prior, exists := normalized[host]; exists && prior != enabled {
			return nil, fmt.Errorf("conflicting visual archive settings for %s", host)
		}
		normalized[host] = enabled
	}
	return normalized, nil
}

func NormalizeVisualRules(rules map[string]string) (map[string]string, error) {
	if len(rules) > 1000 {
		return nil, errors.New("too many visual archive rules")
	}
	normalized := make(map[string]string, len(rules))
	for pattern, mode := range rules {
		if mode != "opt-in" && mode != "opt-out" {
			return nil, errors.New("visual archive rules must be opt-in or opt-out")
		}
		if len(strings.Fields(pattern)) != 1 || strings.TrimSpace(pattern) != pattern {
			return nil, errors.New("visual archive rules require one pattern without spaces")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("invalid visual archive pattern: %w", err)
		}
		normalized[pattern] = mode
	}
	return normalized, nil
}

func visualHostname(value string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if strings.ContainsAny(host, "/:@?#*\\") {
		return "", fmt.Errorf("invalid visual archive hostname %q", value)
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", fmt.Errorf("invalid visual archive hostname %q", value)
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("invalid visual archive hostname %q", value)
		}
		for _, letter := range label {
			if !(letter >= 'a' && letter <= 'z' || letter >= '0' && letter <= '9' || letter == '-') {
				return "", fmt.Errorf("invalid visual archive hostname %q", value)
			}
		}
	}
	return ascii, nil
}

func (c *Config) VisualMode() string {
	if c.App.DisablePreviews || c.VisualArchive.Mode == "disabled" {
		return "disabled"
	}
	c.visualMu.RLock()
	mode := c.visualModeOverride
	c.visualMu.RUnlock()
	if mode == "" {
		mode = c.VisualArchive.Mode
	}
	if mode == "" {
		mode = "opt-out"
	}
	return mode
}

func (c *Config) VisualEnabled(pageURL string, rules *Rules) bool {
	mode := c.VisualMode()
	if mode == "disabled" {
		return false
	}
	parsed, err := url.Parse(pageURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	host, err := visualHostname(parsed.Hostname())
	if err != nil {
		return false
	}
	if rules == nil {
		rules = c.Rules
	}
	if rules != nil && rules.IsSkip(pageURL) {
		return false
	}
	optIn := false
	if rules != nil {
		for pattern, ruleMode := range rules.VisualRules {
			compiled := rules.visualPatterns[pattern]
			if compiled == nil {
				compiled, err = regexp.Compile(pattern)
				if err != nil {
					return false
				}
			}
			if compiled.MatchString(pageURL) {
				if ruleMode == "opt-out" {
					return false
				}
				optIn = optIn || ruleMode == "opt-in"
			}
		}
	}
	if optIn {
		return true
	}
	for candidate := host; candidate != ""; {
		if rules != nil {
			if enabled, exists := rules.VisualSites[candidate]; exists {
				return enabled
			}
		}
		if net.ParseIP(host) != nil {
			break
		}
		_, candidate, _ = strings.Cut(candidate, ".")
	}
	return mode == "opt-out"
}

func (c *Config) SetVisualMode(mode string) error {
	if mode == "" {
		return errors.New("visual archive mode is required")
	}
	if err := ValidateVisualMode(mode); err != nil {
		return err
	}
	if c.App.DisablePreviews || c.VisualArchive.Mode == "disabled" {
		return errors.New("visual archive is disabled in the installation configuration")
	}
	c.visualMu.Lock()
	defer c.visualMu.Unlock()
	if err := writeJSONAtomic(c.FullPath("visual-settings.json"), struct {
		Mode string `json:"mode"`
	}{mode}); err != nil {
		return err
	}
	c.visualModeOverride = mode
	return nil
}

func (c *Config) loadVisualSettings() error {
	data, err := os.ReadFile(c.FullPath("visual-settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	if err := ValidateVisualMode(settings.Mode); err != nil {
		return err
	}
	c.visualMu.Lock()
	c.visualModeOverride = settings.Mode
	c.visualMu.Unlock()
	return nil
}

func writeJSONAtomic(filename string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".hister-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filename)
}
