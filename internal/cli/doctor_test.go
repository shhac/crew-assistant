package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestDoctorDoesNotRequireUnusedExternalResources(t *testing.T) {
	for _, tc := range []struct {
		name           string
		legacy, cli    bool
		wantCredential bool
	}{
		{name: "local_only"},
		{name: "opted_in_legacy", legacy: true, wantCredential: true},
		{name: "cli_resource_supersedes_legacy", legacy: true, cli: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := completionRoot(t)
			cfg := config.Default()
			cfg.Model.Engine = "openai-compatible"
			cfg.Model.Effort = ""
			cfg.Model.Model = "synthetic"
			cfg.Engines.OpenAICompatible.APIKeyEnv = "ASSISTANT_TEST_MODEL_KEY"
			// Team-role checks must not reach the installed CLIs from a test.
			cfg.Engines.Codex.Bin = filepath.Join(t.TempDir(), "missing-codex")
			cfg.Engines.Claude.Bin = filepath.Join(t.TempDir(), "missing-claude")
			cfg.Linear.TeamIDs = []string{"example-team"}
			cfg.Linear.ImportAssignments = tc.legacy
			cfg.Linear.APIKeyEnv = "ASSISTANT_TEST_LINEAR_KEY"
			if tc.cli {
				cfg.Connections = []config.Connection{{ID: "work", Name: "Work", Tool: "lin", Profiles: []string{"company"}}}
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			// The command writes NDJSON to stdout; use a regular temporary file so
			// capturing cannot deadlock on a full pipe. No model or API is called.
			output, err := os.CreateTemp(t.TempDir(), "doctor-output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			original := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = original }()
			root.SetArgs([]string{"--config", path, "doctor"})
			err = root.Execute()
			os.Stdout = original
			if err != nil {
				t.Fatal(err)
			}
			if _, err := output.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(output)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if strings.Contains(text, cfg.Linear.APIKeyEnv) != tc.wantCredential {
				t.Fatalf("unexpected Linear requirement: %s", text)
			}
			if strings.Contains(text, cfg.Slack.BotTokenEnv) || strings.Contains(text, cfg.Slack.AppTokenEnv) {
				t.Fatalf("unused Slack bot required: %s", text)
			}
		})
	}
}
