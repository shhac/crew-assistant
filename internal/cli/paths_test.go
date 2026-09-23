package cli

import (
	"path/filepath"
	"testing"
)

func TestExplicitConfigAndStatePairIsUsed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cmd := NewRoot("test")
	cmd.SetArgs([]string{"--config", filepath.Join(root, "chosen.json"), "--state", filepath.Join(root, "chosen.db"), "config", "validate"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}
