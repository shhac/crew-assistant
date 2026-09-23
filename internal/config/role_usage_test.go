package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoleUsageDefaultsForExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"limits":{"max_model_turns":7}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := RoleUsage{CodexMaxUsedPercent: 90, ClaudeMaxUsedPercent: 90, OnUnavailable: "allow"}
	if got.Limits.RoleUsage != want || got.Limits.MaxModelTurns != 7 {
		t.Fatalf("existing configuration did not receive usage defaults: %+v", got.Limits)
	}
}

func TestRoleUsagePartialOverrideAndDisabledThresholdSurviveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"limits":{"role_usage":{"codex_max_used_percent":0,"on_unavailable":"pause"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := RoleUsage{CodexMaxUsedPercent: 0, ClaudeMaxUsedPercent: 90, OnUnavailable: "pause"}
	if got.Limits.RoleUsage != want {
		t.Fatalf("explicit zero or omitted defaults lost: %+v", got.Limits.RoleUsage)
	}
	if err := Save(path, got); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.Limits.RoleUsage != want {
		t.Fatalf("usage policy changed after save: %+v, %v", reloaded.Limits.RoleUsage, err)
	}
}

func TestRoleUsageValidation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		usage        RoleUsage
		invalidField string
	}{
		{"disabled", RoleUsage{0, 0, "allow"}, ""},
		{"full quota", RoleUsage{100, 100, "pause"}, ""},
		{"negative codex", RoleUsage{-1, 90, "allow"}, "codex_max_used_percent"},
		{"large codex", RoleUsage{101, 90, "allow"}, "codex_max_used_percent"},
		{"negative claude", RoleUsage{90, -1, "allow"}, "claude_max_used_percent"},
		{"large claude", RoleUsage{90, 101, "allow"}, "claude_max_used_percent"},
		{"missing policy", RoleUsage{90, 90, ""}, "on_unavailable"},
		{"unknown policy", RoleUsage{90, 90, "deny"}, "on_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Limits.RoleUsage = tc.usage
			err := cfg.Validate()
			if tc.invalidField == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "limits.role_usage."+tc.invalidField) {
				t.Fatalf("wanted field-specific validation for %s, got %v", tc.invalidField, err)
			}
		})
	}
}
