package config

import (
	"os"
	"sync"
	"testing"
)

func TestInvalidVisualSettingsDisableCaptureWithoutPreventingStartup(t *testing.T) {
	for _, contents := range []string{`{`, `{"mode":"invalid"}`} {
		cfg := CreateDefaultConfig()
		cfg.App.Directory = t.TempDir()
		if err := os.WriteFile(cfg.FullPath("visual-settings.json"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := cfg.init(); err != nil {
			t.Fatal(err)
		}
		if cfg.VisualMode() != "disabled" {
			t.Fatal("invalid settings enabled visual capture")
		}
	}
}

func TestVisualPolicy(t *testing.T) {
	rules := &Rules{VisualSites: map[string]bool{"example.com": false, "docs.example.com": true}}
	for _, test := range []struct {
		mode string
		url  string
		want bool
	}{
		{"disabled", "https://docs.example.com", false},
		{"opt-in", "https://other.example", false},
		{"opt-out", "https://other.example", true},
		{"opt-out", "https://example.com", false},
		{"opt-out", "https://www.example.com", false},
		{"opt-out", "https://notexample.com", true},
		{"opt-in", "https://docs.example.com", true},
		{"opt-in", "https://api.docs.example.com", true},
		{"opt-out", "file:///tmp/note", false},
		{"opt-out", "https://user:pass@example.com", false},
		{"opt-out", "https://", false},
	} {
		t.Run(test.mode+test.url, func(t *testing.T) {
			cfg := CreateDefaultConfig()
			cfg.VisualArchive.Mode = test.mode
			if got := cfg.VisualEnabled(test.url, rules); got != test.want {
				t.Fatalf("enabled=%v, want %v", got, test.want)
			}
		})
	}
}

func TestVisualSitesValidation(t *testing.T) {
	for _, host := range []string{"", "https://example.com", "example.com/path", "*.example.com", "example.com:443", "a..com", "a_b.com", "-a.com", "a-.com", "foo bar.com"} {
		if _, err := NormalizeVisualSites(map[string]bool{host: true}); err == nil {
			t.Errorf("accepted %q", host)
		}
	}
	sites, err := NormalizeVisualSites(map[string]bool{" ExAmPle.COM. ": true, "bücher.example": false})
	if err != nil || !sites["example.com"] || len(sites) != 2 {
		t.Fatalf("sites=%v, err=%v", sites, err)
	}
	if _, ok := sites["xn--bcher-kva.example"]; !ok {
		t.Fatal("IDNA hostname missing")
	}
}

func TestVisualPatternPolicy(t *testing.T) {
	rules := &Rules{VisualRules: map[string]string{
		`(?i)^https?://([a-z0-9-]+\.)*example\.com(:[0-9]+)?(/|$)`: "opt-in",
		`/private(/|$)`:                    "opt-out",
		`^https://other\.test/only\?id=1$`: "opt-in",
	}}
	if err := rules.Compile(); err != nil {
		t.Fatal(err)
	}
	cfg := CreateDefaultConfig()
	cfg.VisualArchive.Mode = "opt-in"
	for _, test := range []struct {
		url  string
		want bool
	}{
		{"https://docs.example.com/public", true},
		{"https://EXAMPLE.COM/public", true},
		{"https://example.com/private/article", false},
		{"https://other.test/only?id=1", true},
		{"https://other.test/only?id=2", false},
		{"https://notexample.com/public", false},
		{"https://other.test/path/example.com", false},
	} {
		if got := cfg.VisualEnabled(test.url, rules); got != test.want {
			t.Errorf("%s: enabled=%v, want %v", test.url, got, test.want)
		}
	}
	if rules.Count() != 0 {
		t.Fatal("visual-only rules changed indexing rule count")
	}
	rules.Skip = &Rule{ReStrs: []string{"/public"}}
	if err := rules.Compile(); err != nil {
		t.Fatal(err)
	}
	if cfg.VisualEnabled("https://example.com/public", rules) {
		t.Fatal("visual opt-in bypassed skip")
	}
	cfg.VisualArchive.Mode = "disabled"
	if cfg.VisualEnabled("https://example.com/other", rules) {
		t.Fatal("visual opt-in bypassed installation disable")
	}
	cfg.VisualArchive.Mode = "opt-out"
	if !cfg.VisualEnabled("https://unmatched.test", rules) {
		t.Fatal("unmatched rules did not inherit opt-out installation mode")
	}
}

func TestVisualPatternValidation(t *testing.T) {
	for _, rules := range []map[string]string{
		{"[": "opt-out"}, {"": "opt-in"}, {"a b": "opt-in"}, {" a": "opt-in"},
		{"example": "--"}, {"example": "disabled"},
	} {
		if _, err := NormalizeVisualRules(rules); err == nil {
			t.Errorf("accepted invalid visual rules: %v", rules)
		}
	}
}

func TestVisualModePersistenceAndHardDisable(t *testing.T) {
	cfg := CreateDefaultConfig()
	cfg.App.Directory = t.TempDir()
	if err := cfg.SetVisualMode("opt-in"); err != nil {
		t.Fatal(err)
	}
	reloaded := CreateDefaultConfig()
	reloaded.App.Directory = cfg.App.Directory
	if err := reloaded.loadVisualSettings(); err != nil || reloaded.VisualMode() != "opt-in" {
		t.Fatalf("mode=%s, err=%v", reloaded.VisualMode(), err)
	}
	if err := reloaded.SetVisualMode("disabled"); err != nil || reloaded.VisualMode() != "disabled" {
		t.Fatalf("mode=%s, err=%v", reloaded.VisualMode(), err)
	}
	if err := reloaded.SetVisualMode("opt-out"); err != nil {
		t.Fatal(err)
	}
	reloaded.VisualArchive.Mode = "disabled"
	if reloaded.VisualMode() != "disabled" || reloaded.SetVisualMode("opt-out") == nil {
		t.Fatal("installation hard disable was bypassed")
	}
	cfg.App.DisablePreviews = true
	if cfg.VisualMode() != "disabled" {
		t.Fatal("disable_previews did not disable visual archive")
	}
}

func TestVisualRulesPersistence(t *testing.T) {
	cfg := CreateDefaultConfig()
	cfg.App.Directory = t.TempDir()
	cfg.Rules = &Rules{VisualSites: map[string]bool{"example.com": true}, VisualRules: map[string]string{`/private`: "opt-out"}}
	if err := cfg.SaveRules(); err != nil {
		t.Fatal(err)
	}
	if !cfg.VisualEnabled("https://example.com", nil) {
		t.Fatal("saved visual site setting was not loaded")
	}
	if cfg.VisualEnabled("https://example.com/private", nil) || cfg.Rules.VisualRules[`/private`] != "opt-out" {
		t.Fatal("saved visual pattern setting was not loaded")
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 10; iteration++ {
				if err := cfg.SetVisualMode("opt-in"); err != nil {
					t.Error(err)
				}
				_ = cfg.VisualMode()
				_ = cfg.VisualEnabled("https://example.com", nil)
			}
		}()
	}
	workers.Wait()
}

func TestVisualRulesCompilationNormalizesSites(t *testing.T) {
	rules := &Rules{VisualSites: map[string]bool{"Example.COM.": true}}
	if err := rules.Compile(); err != nil || !rules.VisualSites["example.com"] {
		t.Fatalf("sites=%v err=%v", rules.VisualSites, err)
	}
	rules.VisualSites = map[string]bool{"https://example.com": true}
	if err := rules.Compile(); err == nil {
		t.Fatal("invalid stored visual hostname accepted")
	}
}
