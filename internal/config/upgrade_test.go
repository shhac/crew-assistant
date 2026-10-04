package config

import (
	"strings"
	"testing"
	"time"
)

func TestUpgradeSettingsDefaultsValidationAndExample(t *testing.T) {
	c := Default()
	if c.Upgrade.Mode != "ask" || c.Upgrade.Interval() != 6*time.Hour {
		t.Fatal(c.Upgrade)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("../../config.example.json"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"off", "ask", "automatic"} {
		c.Upgrade.Mode = mode
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"auto", "unknown"} {
		c.Upgrade.Mode = mode
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "off, ask or automatic") {
			t.Fatal(err)
		}
	}
	c = Default()
	for _, interval := range []string{"14m", "0", "-1h", "banana"} {
		c.Upgrade.CheckInterval = interval
		if c.Validate() == nil {
			t.Fatal(interval)
		}
	}
	c.Upgrade.CheckInterval = "15m"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"file:///tmp/source", "https://user:secret@fixture.invalid/", "https://fixture.invalid/?token=secret", "relative"} {
		c.Upgrade.FormulaURL = raw
		if c.Validate() == nil {
			t.Fatal(raw)
		}
	}
	u := DefaultUpgrade()
	u.SourceRepo = "fixture/application"
	u.Formula = "fixture/tools/application"
	if u.ReleaseURL() != "https://api.github.com/repos/fixture/application/releases/latest" || u.FormulaSourceURL() != "https://raw.githubusercontent.com/fixture/homebrew-tools/HEAD/Formula/application.rb" {
		t.Fatal(u)
	}
	u.ReleaseAPIURL = "https://fixture.invalid/api"
	u.FormulaURL = "https://fixture.invalid/formula"
	if u.ReleaseURL() != u.ReleaseAPIURL || u.FormulaSourceURL() != u.FormulaURL {
		t.Fatal(u)
	}
}
