package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool folder shows the team a toolchain, never the owner's home or the
// places credentials live.
func TestToolFoldersAreToolchainsOnly(t *testing.T) {
	home, _ := os.UserHomeDir()
	for dir, ok := range map[string]bool{
		filepath.Join(home, ".nvm/versions/node/v22.22.3"): true,
		"/opt/homebrew/opt/node@22":                        true,
		home:                                               false,
		"/":                                                false,
		filepath.Dir(home):                                 false,
		filepath.Join(home, ".ssh"):                        false,
		filepath.Join(home, ".config/gh"):                  false,
		filepath.Join(home, "Library"):                     false,
		"relative/node":                                    false,
		filepath.Join(home, ".nvm") + "/../.ssh":           false,
	} {
		err := validTool(dir)
		if (err == nil) != ok {
			t.Errorf("%s: %v", dir, err)
		}
	}
	bad := Playbook{Medium: MediumGit, Repo: "/work/app", BranchPrefix: "crew/", Tools: []string{home}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "too wide") {
		t.Fatalf("a playbook granting the home folder: %v", err)
	}
	if err := (Repository{Path: "/work/app", Tools: []string{filepath.Join(home, ".aws")}}).validate(); err == nil {
		t.Fatal("a repository granting ~/.aws was accepted")
	}
}

// Tools are the repository's, so every project working in it gets them.
func TestAProjectTakesItsRepositorysTools(t *testing.T) {
	v := Snapshot{Repositories: []Repository{{ID: "r", Path: "/work/app", Tools: []string{"/opt/node"}}}}
	p := Project{Settings: &Playbook{Medium: MediumGit}, Scope: Scope{Repositories: []ScopeRepository{{ID: "r"}}}}
	pb, ok := v.basePlaybook(p)
	if !ok || len(pb.Tools) != 1 || pb.Tools[0] != "/opt/node" {
		t.Fatalf("effective tools %v", pb.Tools)
	}
	pb.Tools[0] = "/elsewhere"
	if v.Repositories[0].Tools[0] != "/opt/node" {
		t.Fatal("the effective playbook shares the repository's list")
	}
}
