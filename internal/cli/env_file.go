package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

// An environment file is operator-supplied startup configuration, never
// discovered in a project workspace. Only commands that use credentials load
// it, and a demo refuses it before reading any contents.
func loadCommandEnvFile(cmd *cobra.Command, path string) error {
	if path == "" {
		return nil
	}
	if cmd.Name() != "serve" && cmd.Name() != "doctor" {
		return errors.New("--env-file is only supported by serve and doctor")
	}
	if cmd.Name() == "serve" {
		demo, _ := cmd.Flags().GetBool("demo")
		if demo {
			return errors.New("--env-file cannot be used with --demo")
		}
	}
	return loadEnvFile(path)
}

// Parse and validate the entire file before changing the environment. Parser
// errors can contain secret values, so none of the underlying errors escape.
func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("could not read --env-file; check its path and permissions")
	}
	values, err := godotenv.Unmarshal(string(data))
	if err != nil {
		return errors.New("could not parse --env-file; check its dotenv syntax")
	}
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return errors.New("--env-file contains an invalid environment entry")
		}
	}
	loaded := []string{}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			for _, previous := range loaded {
				_ = os.Unsetenv(previous)
			}
			return errors.New("could not apply --env-file")
		}
		loaded = append(loaded, key)
	}
	return nil
}
