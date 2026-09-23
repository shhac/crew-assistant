package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shhac/crew-assistant/internal/core"
)

// Artifacts are files a worker attempt preserved. The dashboard needs to hand
// them to the owner without the daemon becoming a way to read the filesystem,
// so three rules hold together here:
//
//   - A request names an opaque token, never a path. The token is the digest of
//     an installation secret and the file's absolute path, so it cannot be
//     computed or guessed from a path, and knowing one file's URL does not
//     describe the directory it sits in.
//   - A token is minted only for a path the daemon itself recorded as evidence
//     AND that is one of a run's preserved artifacts. Evidence text is
//     worker-authored, and the state directory also holds the owner's database,
//     this file's own salt, the pairing code and the admin token — so anything
//     outside a run's artifact directory never gets a token.
//   - The file is opened through a root anchored at the state directory, so a
//     symlink or traversal cannot reach outside it even if the recorded path
//     tries to.
//
// Nothing is persisted: tokens are derived from current state, so a link stops
// working once the evidence that justified it is gone.
const maxArtifactBytes = 64 << 20

// artifactPrefixes are the evidence lines that name a preserved file. They are
// the same prefixes the dashboard groups artifacts by, pinned by a broker test.
var artifactPrefixes = []string{
	"Patch: ",
	"Command results: ",
	"Changed-file summary: ",
	"Worker artifacts: ",
}

type artifactSecret struct {
	once  sync.Once
	value []byte
	err   error
}

// salt loads, or creates once, the installation secret that makes a token
// unguessable. It lives beside the daemon's other private state.
func (a *App) artifactSalt() ([]byte, error) {
	a.artifactKey.once.Do(func() {
		path := filepath.Join(a.Core.StateDirectory(), "artifact-salt")
		existing, err := os.ReadFile(path)
		if err == nil && len(existing) >= 32 {
			a.artifactKey.value = existing
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			a.artifactKey.err = err
			return
		}
		fresh := make([]byte, 32)
		if _, err = rand.Read(fresh); err != nil {
			a.artifactKey.err = err
			return
		}
		if err = os.WriteFile(path, fresh, 0600); err != nil {
			a.artifactKey.err = err
			return
		}
		a.artifactKey.value = fresh
	})
	return a.artifactKey.value, a.artifactKey.err
}

func (a *App) artifactToken(path string) (string, error) {
	salt, err := a.artifactSalt()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(append([]byte{}, salt...), []byte(path)...))
	return hex.EncodeToString(sum[:]), nil
}

// artifactPath reports the file an evidence line names, if it names one.
func artifactPath(line string) string {
	for _, prefix := range artifactPrefixes {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// artifactRoot is where a managed worker preserves a run's files, relative to
// the daemon's state directory: managed-workers/<project>/broker/runs/<id>/artifacts.
var artifactRoot = []string{"managed-workers", "", "broker", "runs", "", "artifacts"}

// withinArtifacts reports whether a recorded path is one of a run's preserved
// files. Evidence is worker-authored, so this is the rule that decides what the
// daemon will serve — and "somewhere under the state directory" is far too
// wide: that directory also holds the owner's database, this file's own salt,
// the pairing code and the admin token. Only run artifact directories qualify.
func withinArtifacts(root, path string) (string, bool) {
	if root == "" || !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < len(artifactRoot) {
		return "", false
	}
	for i, want := range artifactRoot {
		// An empty pattern segment is a name the daemon chose (a project or a
		// run); it may be anything except a traversal.
		if want == "" {
			if parts[i] == "" || parts[i] == "." || parts[i] == ".." {
				return "", false
			}
			continue
		}
		if parts[i] != want {
			return "", false
		}
	}
	return rel, true
}

// artifactCandidates lists the paths this snapshot makes downloadable: those an
// attempt recorded that are one of a run's preserved artifacts.
func artifactCandidates(s core.Snapshot, root string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, agent := range s.Agents {
		for _, line := range agent.Evidence {
			path := artifactPath(line)
			if path == "" || seen[path] {
				continue
			}
			if _, ok := withinArtifacts(root, path); !ok {
				continue
			}
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

// ArtifactLinks maps each downloadable artifact path to its token. A failure to
// read the installation secret is reported rather than returned as an empty
// map, which would be indistinguishable from an attempt that recorded nothing.
func (a *App) ArtifactLinks(s core.Snapshot) (map[string]string, error) {
	out := map[string]string{}
	for _, path := range artifactCandidates(s, a.Core.StateDirectory()) {
		token, err := a.artifactToken(path)
		if err != nil {
			return nil, err
		}
		out[path] = token
	}
	return out, nil
}

var errArtifactNotFound = errors.New("artifact not found")

// OpenArtifact resolves a token against the artifacts current evidence names
// and opens the file through a root anchored at the state directory. A
// directory, an irregular file, or anything larger than the cap is refused.
func (a *App) OpenArtifact(s core.Snapshot, token string) (string, io.ReadCloser, int64, error) {
	if len(token) != sha256.Size*2 {
		return "", nil, 0, errArtifactNotFound
	}
	root := a.Core.StateDirectory()
	match := ""
	for _, path := range artifactCandidates(s, root) {
		candidate, err := a.artifactToken(path)
		if err != nil {
			return "", nil, 0, err
		}
		// Compared in constant time so a wrong token cannot be narrowed by
		// timing the response.
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			match = path
		}
	}
	if match == "" {
		return "", nil, 0, errArtifactNotFound
	}
	rel, ok := withinArtifacts(root, match)
	if !ok {
		return "", nil, 0, errArtifactNotFound
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return "", nil, 0, err
	}
	defer handle.Close()
	file, err := handle.Open(rel)
	if err != nil {
		return "", nil, 0, errArtifactNotFound
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return "", nil, 0, errArtifactNotFound
	}
	if info.Size() > maxArtifactBytes {
		file.Close()
		return "", nil, 0, errors.New("artifact is too large to serve")
	}
	return filepath.Base(match), file, info.Size(), nil
}
