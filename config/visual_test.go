package config

import (
	"sync"
	"testing"
)

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

func TestVisualRulesSnapshots(t *testing.T) {
	cfg := CreateDefaultConfig()
	cfg.App.Directory = t.TempDir()
	if err := cfg.SaveRulesSnapshot(&Rules{VisualSites: map[string]bool{"example.com": true}}); err != nil {
		t.Fatal(err)
	}
	snapshot := cfg.RulesSnapshot()
	snapshot.VisualSites["example.com"] = false
	if !cfg.VisualEnabled("https://example.com", nil) {
		t.Fatal("snapshot changed live rules")
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
				if err := cfg.SaveRulesSnapshot(snapshot); err != nil {
					t.Error(err)
				}
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
