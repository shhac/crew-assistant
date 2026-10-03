package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// UpgradeSettings controls notices only. Installation remains an owner action.
type UpgradeSettings struct {
	Mode          string `json:"mode"`
	CheckInterval string `json:"check_interval"`
	SourceRepo    string `json:"source_repo"`
	Formula       string `json:"formula"`
	ReleaseAPIURL string `json:"release_api_url"`
	FormulaURL    string `json:"formula_url"`
}

func DefaultUpgrade() UpgradeSettings {
	return UpgradeSettings{Mode: "ask", CheckInterval: "6h", SourceRepo: "shhac/crew-assistant", Formula: "shhac/tap/crew-assistant"}
}

// Empty URL overrides follow the configured repository and formula.
func (u UpgradeSettings) ReleaseURL() string {
	if u.ReleaseAPIURL != "" {
		return u.ReleaseAPIURL
	}
	return "https://api.github.com/repos/" + u.SourceRepo + "/releases/latest"
}
func (u UpgradeSettings) FormulaSourceURL() string {
	if u.FormulaURL != "" {
		return u.FormulaURL
	}
	p := strings.Split(u.Formula, "/")
	if len(p) != 3 {
		return ""
	}
	return "https://raw.githubusercontent.com/" + p[0] + "/homebrew-" + p[1] + "/HEAD/Formula/" + p[2] + ".rb"
}
func (u UpgradeSettings) Interval() time.Duration {
	d, _ := time.ParseDuration(u.CheckInterval)
	return d
}
func (u UpgradeSettings) validate() error {
	if u.Mode == "auto" || u.Mode == "automatic" {
		return fmt.Errorf("upgrade.mode: automatic upgrades aren't available yet; choose off or ask")
	}
	if u.Mode != "off" && u.Mode != "ask" {
		return fmt.Errorf("upgrade.mode must be off or ask")
	}
	if d, err := time.ParseDuration(u.CheckInterval); err != nil || d < 15*time.Minute {
		return fmt.Errorf("upgrade.check_interval must be a duration of at least 15m")
	}
	part := `[A-Za-z0-9][A-Za-z0-9_.-]*`
	if !regexp.MustCompile(`^` + part + `/` + part + `$`).MatchString(u.SourceRepo) {
		return fmt.Errorf("upgrade.source_repo must be owner/name")
	}
	if !regexp.MustCompile(`^` + part + `/` + part + `/` + part + `$`).MatchString(u.Formula) {
		return fmt.Errorf("upgrade.formula must be owner/tap/formula")
	}
	for _, raw := range []string{u.ReleaseAPIURL, u.FormulaURL} {
		if raw == "" {
			continue
		}
		v, err := url.Parse(raw)
		if err != nil || (v.Scheme != "https" && v.Scheme != "http") || v.Host == "" || v.User != nil || v.RawQuery != "" || v.Fragment != "" {
			return fmt.Errorf("upgrade source URLs must be HTTP(S) URLs without credentials, query or fragment")
		}
	}
	return nil
}
