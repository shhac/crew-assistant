package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func envFileFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.env")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func absentTestEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // Register restoration of the original value.
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestEnvFileLoadsDotenvWithoutReplacingExistingEnvironment(t *testing.T) {
	absentTestEnv(t, "CREW_ENV_TEST_TOKEN")
	t.Setenv("CREW_ENV_TEST_EXISTING", "existing")
	t.Setenv("CREW_ENV_TEST_EMPTY", "")
	path := envFileFixture(t, "# private startup configuration\nexport CREW_ENV_TEST_TOKEN='synthetic $literal # token'\nCREW_ENV_TEST_EXISTING=from-file\nCREW_ENV_TEST_EMPTY=from-file\n")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CREW_ENV_TEST_TOKEN") != "synthetic $literal # token" {
		t.Fatal("quoted dotenv value was not loaded")
	}
	if os.Getenv("CREW_ENV_TEST_EXISTING") != "existing" || os.Getenv("CREW_ENV_TEST_EMPTY") != "" {
		t.Fatal("the existing environment must take precedence, including empty values")
	}
}

func TestInvalidEnvFileChangesNothingAndNeverExposesContents(t *testing.T) {
	for _, contents := range []string{
		"CREW_ENV_TEST_NEW=synthetic-token\nsecret-invalid-name!=sensitive-value\n",
		"CREW_ENV_TEST_NEW=synthetic-token\nCREW_ENV_TEST_BAD='sensitive-value\n",
		"CREW_ENV_TEST_NEW=synthetic-token\nCREW_ENV_TEST_BAD='sensitive\x00value'\n",
	} {
		absentTestEnv(t, "CREW_ENV_TEST_NEW")
		absentTestEnv(t, "CREW_ENV_TEST_BAD")
		err := loadEnvFile(envFileFixture(t, contents))
		if err == nil {
			t.Fatal("invalid dotenv file accepted")
		}
		if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "secret-invalid-name") || strings.Contains(err.Error(), "synthetic-token") {
			t.Fatal("an env-file error exposed file contents")
		}
		if _, exists := os.LookupEnv("CREW_ENV_TEST_NEW"); exists {
			t.Fatal("invalid file partially changed the environment")
		}
	}
	if err := loadEnvFile(filepath.Join(t.TempDir(), "private-token-in-path")); err == nil || strings.Contains(err.Error(), "private-token-in-path") {
		t.Fatal("read error must stay generic")
	}
}

func TestEnvFileFlagOnlyLoadsForServeAndDoctor(t *testing.T) {
	for _, command := range []string{"serve", "doctor", "status"} {
		t.Run(command, func(t *testing.T) {
			absentTestEnv(t, "CREW_ENV_TEST_STARTUP")
			root := NewRoot("test")
			cmd, _, err := root.Find([]string{command})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			cmd.RunE = func(*cobra.Command, []string) error {
				called = true
				if os.Getenv("CREW_ENV_TEST_STARTUP") != "synthetic-token" {
					t.Fatal("command ran before loading its environment")
				}
				return nil
			}
			root.SetArgs([]string{"--env-file", envFileFixture(t, "CREW_ENV_TEST_STARTUP=synthetic-token\n"), command})
			err = root.Execute()
			if command == "status" {
				if err == nil || called {
					t.Fatal("a client command loaded startup credentials")
				}
				if _, exists := os.LookupEnv("CREW_ENV_TEST_STARTUP"); exists {
					t.Fatal("client command changed the environment")
				}
			} else if err != nil || !called {
				t.Fatalf("startup command failed: %v", err)
			}
		})
	}
}

func TestDemoRefusesEnvFileBeforeReadingIt(t *testing.T) {
	absentTestEnv(t, "CREW_ENV_TEST_DEMO")
	root := NewRoot("test")
	root.SetArgs([]string{"serve", "--demo", "--env-file", envFileFixture(t, "CREW_ENV_TEST_DEMO=synthetic-token\n")})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--demo") {
		t.Fatalf("demo accepted startup credentials: %v", err)
	}
	if _, exists := os.LookupEnv("CREW_ENV_TEST_DEMO"); exists {
		t.Fatal("demo loaded startup credentials")
	}
}
