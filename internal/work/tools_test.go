package work

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// A granted toolchain is readable and its bin folder comes first on PATH;
// a folder that isn't there is left out.
func TestGrantedToolsAreReadableAndFirstOnPath(t *testing.T) {
	node := t.TempDir()
	if err := os.Mkdir(filepath.Join(node, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	m := gitMedium{playbook: core.Playbook{Tools: []string{node, cache, filepath.Join(node, "missing")}}}
	read := m.readable()
	if !slices.Contains(read, node) || !slices.Contains(read, cache) || slices.Contains(read, filepath.Join(node, "missing")) {
		t.Fatalf("readable %v", read)
	}
	path := m.toolPath()
	want := "PATH=" + filepath.Join(node, "bin") + string(os.PathListSeparator) + cache + string(os.PathListSeparator)
	if len(path) != 1 || !strings.HasPrefix(path[0], want) || !strings.HasSuffix(path[0], os.Getenv("PATH")) {
		t.Fatalf("path %v, want it to start %q and end with the daemon's", path, want)
	}
	if (gitMedium{}).toolPath() != nil {
		t.Fatal("no tools still changed PATH")
	}
}
