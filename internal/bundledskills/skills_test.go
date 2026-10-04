package bundledskills

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
)

func TestCatalogAndGuidance(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Catalog() {
		if seen[e.Name] || !Known(e.Name) || e.Description == "" {
			t.Fatal(e)
		}
		seen[e.Name] = true
		data, err := files.ReadFile(e.Name + "/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, want := range []string{"attach_file", "generated", "rejected: true", "asset", "rejected-variants.zip", "untouched bytes"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing supported attachment guidance %q", e.Name, want)
			}
		}
		for _, want := range []string{"never assume a neat grid", "connected components of the alpha or chroma mask", "exactly the expected figure count", "no figure touching the strip edge or another figure", "each figure's actual bounding box, never equal slots", "one uniform scale and a shared anchor", "fit with margin", "Refuse overflow rather than clipping", "original generated strip of every attempt, including failed attempts"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing owner guidance %q", e.Name, want)
			}
		}
		for _, want := range []string{"name: " + e.Name, "description: " + e.Description, "hatch-pet", "Apache-2.0", "no upstream code or assets are copied", "production", "provenance", "contact sheet", "motion preview", "canonical", "anchor", "4–12", "two failed attempts"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing %q", e.Name, want)
			}
		}
		children, _ := files.ReadDir(e.Name)
		if len(children) != 1 || children[0].Name() != "SKILL.md" || strings.Contains(text, "```") {
			t.Fatal("skills must contain only prose", e.Name)
		}
	}
	data, _ := files.ReadFile("sprite-atlas/SKILL.md")
	for _, want := range []string{"single image", "canonical base image", "layout guide", "guide never appears", "part continuity", "left wing", "chroma-key", "No shadows", "Idle is subtle", "doubled seam frame", "one facing", "mirrors at runtime", "exact prompt", "repository-write", "stop"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("designer missing %q", want)
		}
	}
}

func TestDirectoryPermissionSemantics(t *testing.T) {
	for _, mask := range []os.FileMode{0077, 0022} {
		for _, mode := range []os.FileMode{0777, 0555} {
			if !safeDirectoryPermissions(os.ModeDir|mode, mask, "windows") {
				t.Fatal("Windows directory attributes rejected", mode)
			}
		}
		if safeDirectoryPermissions(os.ModeDir|0777, mask, "linux") || !safeDirectoryPermissions(os.ModeDir|0700, mask, "darwin") {
			t.Fatal("POSIX permission validation changed")
		}
	}
}

func TestRoleSelectionAndStablePublication(t *testing.T) {
	state := t.TempDir()
	for role, want := range map[string]string{"designer": "sprite-atlas", "implementer": "sprite-atlas-pipeline", "researcher": "", "reviewer": "", "qa": "", "pm": ""} {
		t.Run(role, func(t *testing.T) {
			first, digest, err := Prepare(state, role, nil)
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				if len(first.Provided) != 0 || digest != "" || first.Delivery != "" {
					t.Fatal(first, digest)
				}
				return
			}
			if len(first.Provided) != 1 || first.Provided[0].Name != want || first.Provided[0].Scripts || first.Delivery != harness.SkillDeliveryComposed || first.Global != harness.GlobalSkillsDefault {
				t.Fatal(first)
			}
			for round := 0; round < 3; round++ {
				second, next, err := Prepare(state, role, []string{"irrelevant"})
				if err != nil || !reflect.DeepEqual(first, second) || next != digest {
					t.Fatal(second, next, err)
				}
			}
			disabled, next, err := Prepare(state, role, []string{want})
			if err != nil || len(disabled.Provided) != 0 || next != "" {
				t.Fatal(disabled, next, err)
			}
			info, err := os.Stat(filepath.Join(first.Provided[0].Dir, "SKILL.md"))
			if err != nil || info.Mode().Perm()&0222 != 0 {
				t.Fatal(info, err)
			}
		})
	}
}

func TestPublicationInterruptionCorruptionAndUnsafeStorage(t *testing.T) {
	data, _ := files.ReadFile("sprite-atlas/SKILL.md")
	root := t.TempDir()
	fail := errors.New("interrupted")
	if _, err := publish(root, "sprite-atlas", data, func() error { return fail }); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	children, _ := os.ReadDir(root)
	if len(children) != 0 {
		t.Fatal("incomplete publication", children)
	}
	dir, err := publish(root, "sprite-atlas", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed := append(append([]byte(nil), data...), []byte("\nNew guidance.\n")...)
	other, err := publish(root, "sprite-atlas", changed, nil)
	if err != nil || other == dir {
		t.Fatal(other, err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := publish(root, "sprite-atlas", data, nil); err == nil {
		t.Fatal("accepted corrupt cache")
	}
	if _, err := publish(root, "../escape", data, nil); err == nil {
		t.Fatal("accepted unsafe name")
	}
	if _, err := publish(root, "sprite-atlas", []byte("wrong manifest"), nil); err == nil {
		t.Fatal("accepted malformed manifest")
	}
	state := t.TempDir()
	if err := os.Symlink(root, filepath.Join(state, "bundled-skills")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Prepare(state, "designer", nil); err == nil {
		t.Fatal("followed storage symlink")
	}
	if _, _, err := Prepare(filepath.Join(state, "missing"), "designer", nil); err == nil {
		t.Fatal("ignored missing storage")
	}
}

func TestConcurrentPublicationAndLinkedCacheRefusal(t *testing.T) {
	state := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := Prepare(state, "designer", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	set, _, err := Prepare(state, "designer", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := set.Provided[0].Dir
	path := filepath.Join(dir, "SKILL.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(state, "external")
	if err := os.WriteFile(target, []byte("synthetic"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Prepare(state, "designer", nil); err == nil {
		t.Fatal("accepted linked skill file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Prepare(state, "designer", nil); err == nil {
		t.Fatal("accepted linked skill directory")
	}
}
