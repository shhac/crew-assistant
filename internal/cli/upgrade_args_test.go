package cli

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpgradeProcessArgs(t *testing.T) {
	abs := func(p string) string {
		t.Helper()
		path, err := filepath.Abs(p)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	s, c := abs("current-state"), abs("current-config")
	tests := []struct {
		name       string
		args, want []string
	}{
		{"absent", []string{"serve"}, []string{"serve", "--state", s, "--config", c}},
		{"space", []string{"serve", "--state", "old", "--config", "old"}, []string{"serve", "--state", s, "--config", c}},
		{"equals", []string{"serve", "--state=old", "--config=old"}, []string{"serve", "--state", s, "--config", c}},
		{"mixed", []string{"serve", "--config=old", "--state", "old"}, []string{"serve", "--config", c, "--state", s}},
		{"duplicates", []string{"serve", "--state", "S", "--config", "C", "--state", "S", "--config", "C"}, []string{"serve", "--state", s, "--config", c}},
		{"mixed duplicates", []string{"serve", "--state=old", "--no-dispatch", "--state", "older", "--config=old", "--config", "older"}, []string{"serve", "--state", s, "--no-dispatch", "--config", c}},
		{"state only", []string{"serve", "--state", "old"}, []string{"serve", "--state", s, "--config", c}},
		{"config only", []string{"serve", "--config", "old"}, []string{"serve", "--config", c, "--state", s}},
		{"order and env", []string{"--config", "old", "serve", "--no-dispatch", "--state=old", "--addr", "x", "--env-file", "rel"}, []string{"--config", c, "serve", "--no-dispatch", "--state", s, "--addr", "x", "--env-file", abs("rel")}},
		{"env equals", []string{"serve", "--env-file=rel"}, []string{"serve", "--env-file=" + abs("rel"), "--state", s, "--config", c}},
		{"open", []string{"serve", "--open", "--no-dispatch", "--open=true"}, []string{"serve", "--no-dispatch", "--state", s, "--config", c}},
		{"trailing state", []string{"serve", "--state"}, []string{"serve", "--state", s, "--config", c}},
		{"terminator", []string{"serve", "--state=old", "--config=old", "--", "--state=literal", "--config", "literal", "--env-file=literal"}, []string{"serve", "--state", s, "--config", c, "--", "--state=literal", "--config", "literal", "--env-file=literal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string(nil), tt.args...)
			got := upgradeProcessArgs(&options{statePath: "current-state", configPath: "current-config"}, tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(tt.args, original) {
				t.Fatal("modified input arguments")
			}
		})
	}
}

func TestUpgradeProcessArgsConsecutiveHandovers(t *testing.T) {
	args := []string{"serve", "--no-dispatch", "--state", "old", "--config", "old"}
	for _, paths := range [][2]string{{"first-state", "first-config"}, {"second-state", "second-config"}} {
		s, err := filepath.Abs(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		c, err := filepath.Abs(paths[1])
		if err != nil {
			t.Fatal(err)
		}
		args = upgradeProcessArgs(&options{statePath: paths[0], configPath: paths[1]}, args)
		want := []string{"serve", "--no-dispatch", "--state", s, "--config", c}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("handover with %q: got %q, want %q", paths, args, want)
		}
	}
}
