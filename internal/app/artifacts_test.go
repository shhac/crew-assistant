package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func artifactFixture(t *testing.T) (*App, string) {
	t.Helper()
	a := testApp(t)
	root := a.Core.StateDirectory()
	dir := filepath.Join(root, "managed-workers", "p1", "broker", "runs", "run-1", "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "changes.patch"), []byte("diff --git a/a b/a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return a, dir
}

func links(t *testing.T, a *App, s core.Snapshot) map[string]string {
	t.Helper()
	out, err := a.ArtifactLinks(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func snapshotWithEvidence(evidence ...string) core.Snapshot {
	return core.Snapshot{Agents: []core.Agent{{ID: "a1", Evidence: evidence}}}
}

func TestArtifactTokenIsOpaqueAndPathSpecific(t *testing.T) {
	a, dir := artifactFixture(t)
	patch := filepath.Join(dir, "changes.patch")
	minted := links(t, a, snapshotWithEvidence("Patch: "+patch))
	token := minted[patch]
	if token == "" {
		t.Fatal("no token minted for a recorded artifact")
	}
	if strings.Contains(token, patch) || strings.Contains(token, "changes.patch") {
		t.Fatalf("token discloses the path: %q", token)
	}
	// Another installation must not produce the same token for the same path.
	other, _ := artifactFixture(t)
	if links(t, other, snapshotWithEvidence("Patch: "+patch))[patch] == token {
		t.Fatal("token is derived without an installation secret")
	}
	// A sibling in the same directory gets an unrelated token, so knowing one
	// file's URL does not describe the directory around it.
	sibling := filepath.Join(dir, "commands.json")
	if err := os.WriteFile(sibling, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	siblingToken := links(t, a, snapshotWithEvidence("Command results: "+sibling))[sibling]
	if siblingToken == "" || siblingToken == token {
		t.Fatal("sibling artifacts share a token")
	}
}

func TestArtifactTokenIsStableAcrossSnapshots(t *testing.T) {
	a, dir := artifactFixture(t)
	patch := filepath.Join(dir, "changes.patch")
	first := links(t, a, snapshotWithEvidence("Patch: "+patch))[patch]
	second := links(t, a, snapshotWithEvidence("Patch: "+patch))[patch]
	if first == "" || first != second {
		t.Fatal("a link changes between reads of the same state")
	}
}

// Evidence text is worker-authored. A run that names a file outside the
// daemon's own state must never be offered for download, or the dashboard
// becomes a way to read the owner's filesystem.
func TestArtifactsOutsideTheStateDirectoryAreNeverOffered(t *testing.T) {
	a, _ := artifactFixture(t)
	outside := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(outside, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"Patch: " + outside,
		"Patch: /etc/passwd",
		"Patch: " + filepath.Join(a.Core.StateDirectory(), "..", "escape.txt"),
		"Patch: relative/path.txt",
		"Patch: ",
	} {
		links := links(t, a, snapshotWithEvidence(line))
		if len(links) != 0 {
			t.Fatalf("minted a link for %q: %v", line, links)
		}
	}
}

func TestOpenArtifactRefusesAnythingItDidNotMint(t *testing.T) {
	a, dir := artifactFixture(t)
	patch := filepath.Join(dir, "changes.patch")
	state := snapshotWithEvidence("Patch: " + patch)
	good := links(t, a, state)[patch]

	name, file, size, err := a.OpenArtifact(state, good)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(file)
	file.Close()
	if name != "changes.patch" || size != int64(len(body)) || !strings.HasPrefix(string(body), "diff --git") {
		t.Fatalf("served the wrong content: %q %d %q", name, size, body)
	}

	// Flipping the final character must actually change it, whatever it is.
	flipped := "0"
	if strings.HasSuffix(good, "0") {
		flipped = "1"
	}
	for _, bad := range []string{
		"",
		"not-a-token",
		strings.Repeat("a", 64),
		good[:len(good)-1] + flipped,
	} {
		if _, _, _, err := a.OpenArtifact(state, bad); err == nil {
			t.Fatalf("served a file for token %q", bad)
		}
	}

	// A token stops working once the evidence that justified it is gone.
	if _, _, _, err := a.OpenArtifact(core.Snapshot{}, good); err == nil {
		t.Fatal("a link outlived the evidence that justified it")
	}
}

// The recorded path is opened through a root anchored at the state directory,
// so a symlink planted inside it cannot reach outside.
func TestOpenArtifactDoesNotFollowSymlinksOutOfTheStateDirectory(t *testing.T) {
	a, dir := artifactFixture(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "summary.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	state := snapshotWithEvidence("Changed-file summary: " + link)
	token := links(t, a, state)[link]
	if token == "" {
		t.Fatal("expected a token for a path inside the state directory")
	}
	if _, _, _, err := a.OpenArtifact(state, token); err == nil {
		t.Fatal("followed a symlink out of the state directory")
	}
}

func TestOpenArtifactRefusesDirectoriesAndOversizedFiles(t *testing.T) {
	a, dir := artifactFixture(t)
	state := snapshotWithEvidence("Worker artifacts: " + dir)
	token := links(t, a, state)[dir]
	if token == "" {
		t.Fatal("expected a token for the artifacts directory line")
	}
	if _, _, _, err := a.OpenArtifact(state, token); err == nil {
		t.Fatal("served a directory as a file")
	}
}

// The state directory holds the owner's database, this feature's own salt, the
// pairing code and the admin token. Evidence text is worker-authored, so a run
// naming one of them must never produce a download link.
func TestDaemonPrivateFilesAreNeverOffered(t *testing.T) {
	a, _ := artifactFixture(t)
	root := a.Core.StateDirectory()
	for _, name := range []string{
		"state.db",
		"artifact-salt",
		"pairing-code",
		"config.json",
		filepath.Join("state.db.runtime", "admin-token"),
		filepath.Join("managed-workers", "p1", "broker", "broker.json"),
		filepath.Join("managed-workers", "p1", "broker", "docker-config"),
		filepath.Join("projects", "p1", "notes.txt"),
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("SECRET"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := links(t, a, snapshotWithEvidence("Patch: "+path)); len(got) != 0 {
			t.Fatalf("offered a daemon-private file %q: %v", name, got)
		}
	}
}

// A directory whose name merely starts with the state directory's is outside it.
func TestSiblingPrefixDirectoryIsNotInsideTheStateDirectory(t *testing.T) {
	a, _ := artifactFixture(t)
	root := a.Core.StateDirectory()
	outside := root + "-elsewhere/managed-workers/p1/broker/runs/r/artifacts/x"
	if got := links(t, a, snapshotWithEvidence("Patch: "+outside)); len(got) != 0 {
		t.Fatalf("a sibling directory was treated as inside: %v", got)
	}
}

func TestOversizedArtifactIsRefused(t *testing.T) {
	a, dir := artifactFixture(t)
	big := filepath.Join(dir, "huge.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: no real bytes are written.
	if err := f.Truncate(maxArtifactBytes + 1); err != nil {
		f.Close()
		t.Skipf("sparse files unavailable: %v", err)
	}
	f.Close()
	state := snapshotWithEvidence("Patch: " + big)
	token := links(t, a, state)[big]
	if token == "" {
		t.Fatal("expected a token for a file inside the artifacts directory")
	}
	if _, _, _, err := a.OpenArtifact(state, token); err == nil {
		t.Fatal("served a file past the size cap")
	}
}

func TestArtifactSaltIsPrivate(t *testing.T) {
	a, _ := artifactFixture(t)
	if _, err := a.artifactToken("/anything"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(a.Core.StateDirectory(), "artifact-salt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("salt mode = %v, want 0600: it is the only thing making a token unguessable", info.Mode().Perm())
	}
}
