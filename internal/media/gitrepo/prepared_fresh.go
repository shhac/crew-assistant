package gitrepo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// lockfiles say which dependencies a prepared folder, such as node_modules,
// was installed for.
var lockfiles = []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "Gemfile.lock", "poetry.lock", "uv.lock", "Cargo.lock", "composer.lock"}

// lockStamp fingerprints the lockfiles nearest a prepared folder in the
// checkout at root, looking up from the folder's own directory, as a
// workspace's node_modules shares the lockfile at the repository's root. It
// is empty when no lockfile says what the folder holds.
func lockStamp(root, rel string) string {
	for dir := filepath.Dir(rel); ; dir = filepath.Dir(dir) {
		h := sha256.New()
		found := false
		for _, name := range lockfiles {
			if b, err := os.ReadFile(filepath.Join(root, dir, name)); err == nil {
				found = true
				fmt.Fprintf(h, "%s\x00%d\x00", name, len(b))
				h.Write(b)
			}
		}
		if found {
			return hex.EncodeToString(h.Sum(nil))
		}
		if dir == "." || dir == string(filepath.Separator) {
			return ""
		}
	}
}

// stampsPath keeps, in the clone's own git directory where no build sees it,
// the owner's lockfile fingerprint each prepared folder was copied at.
func stampsPath(dir string) string { return filepath.Join(dir, ".git", "crew-prepared.json") }

func readStamps(dir string) map[string]string {
	stamps := map[string]string{}
	if b, err := os.ReadFile(stampsPath(dir)); err == nil {
		_ = json.Unmarshal(b, &stamps)
	}
	return stamps
}

func writeStamps(dir string, stamps map[string]string) error {
	b, err := json.Marshal(stamps)
	if err != nil {
		return err
	}
	return os.WriteFile(stampsPath(dir), b, 0o600)
}

// insideReal reports whether every folder from dir down to path's parent is
// a real folder: a role may have replaced one with a link, which a copy
// would follow out of the clone.
func insideReal(dir, path string) bool {
	for p := filepath.Dir(path); p != dir; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !st.IsDir() {
			return false
		}
		if p == filepath.Dir(p) {
			return false
		}
	}
	return true
}
