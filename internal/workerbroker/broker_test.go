package workerbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

const fixtureImage = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeDocker struct {
	mu        sync.Mutex
	calls     [][]string
	workspace string
	stopped   bool
}

func (d *fakeDocker) Run(ctx context.Context, args []string, input []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, append([]string{}, args...))
	if len(args) > 1 && args[0] == "image" {
		return []byte(fixtureImage), nil
	}
	if args[0] == "run" {
		for i, a := range args {
			if a == "--mount" {
				mount := args[i+1]
				d.workspace = strings.TrimSuffix(strings.TrimPrefix(mount, "type=bind,src="), ",dst=/workspace")
			}
		}
		d.stopped = false
		return []byte("container-id"), nil
	}
	if args[0] == "rm" {
		d.stopped = true
		return nil, nil
	}
	if args[0] == "exec" {
		if contains(args, "--interactive") {
			p := args[len(args)-1]
			if !strings.HasPrefix(p, "/workspace/") {
				return nil, errors.New("outside fake workspace")
			}
			target := filepath.Join(d.workspace, strings.TrimPrefix(p, "/workspace/"))
			_ = os.MkdirAll(filepath.Dir(target), 0755)
			return nil, os.WriteFile(target, input, 0644)
		}
		if args[len(args)-2] == "-c" {
			return []byte("PASS: synthetic verification\n"), nil
		}
		p := args[len(args)-1]
		return os.ReadFile(filepath.Join(d.workspace, strings.TrimPrefix(p, "/workspace/")))
	}
	if args[0] == "container" {
		if args[1] == "inspect" {
			id := strings.TrimPrefix(args[len(args)-1], "agent-assistant-")
			return []byte(strings.Repeat("b", 64) + " " + id), nil
		}
		return nil, nil
	}
	return nil, nil
}
func newFixture(t *testing.T, endpoint string, d *fakeDocker) (*Broker, Config) {
	t.Helper()
	t.Setenv("WORKER_TEST_TOKEN", "fixture-auth")
	t.Setenv("WORKER_TEST_MODEL_KEY", "fixture-model-secret")
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "hello.txt"), []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateDir: filepath.Join(dir, "state"), Workspace: source, ProjectID: "project-one", Image: fixtureImage, ModelEndpoint: endpoint, Model: "fixture-model", APIKeyEnv: "WORKER_TEST_MODEL_KEY", TokenEnv: "WORKER_TEST_TOKEN", Command: d}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b, cfg
}
func startRequest() worker.StartRequest {
	return worker.StartRequest{DispatchKey: "dispatch-one", AgentID: "agent-one", ProjectID: "project-one", Role: "worker", Task: "Update the greeting", AcceptanceCriteria: "Greeting says after and synthetic check passes", Capabilities: []string{"implement"}, Prohibitions: []string{"deployment", "production_data_access", "purchases"}}
}
func request(t *testing.T, b *Broker, path, key string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(value)
	r := httptest.NewRequest("POST", "http://broker.test"+path, strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer fixture-auth")
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}

// The container a worker runs in is offline, unprivileged, read-only and
// carries none of the operator's environment. This reads the arguments the
// daemon actually issues; whether a worker can then get anything done inside
// one is what the integration tests cover.
func TestTheWorkerContainerIsOfflineUnprivilegedAndCredentialFree(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "private-key")
	b, cfg := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := startedRun(t, b)
	r, _ := b.snapshot(id)
	r.WorkDir = filepath.Join(t.TempDir(), "run-copy")
	call := b.containerArgs(r)

	for _, pair := range [][]string{{"--network", "none"}, {"--cap-drop", "ALL"}, {"--security-opt", "no-new-privileges"}} {
		found := false
		for i, v := range call {
			if v == pair[0] && i+1 < len(call) && call[i+1] == pair[1] {
				found = true
			}
		}
		if !found {
			t.Errorf("missing isolation %v", pair)
		}
	}
	if !contains(call, "--pull=never") {
		t.Error("the image may be pulled")
	}
	if !contains(call, "--read-only") {
		t.Error("the root filesystem is writable")
	}
	joined := strings.Join(call, " ")
	if strings.Contains(joined, "docker.sock") || strings.Contains(joined, "private-key") || strings.Contains(joined, "fixture-model-secret") {
		t.Fatal("a host socket or credential was mounted")
	}
	// The worker edits a copy. The project folder itself is never mounted.
	if !strings.Contains(joined, "src="+r.WorkDir+",dst=/workspace") {
		t.Fatalf("the isolated copy was not the mounted workspace: %s", joined)
	}
	if strings.Contains(joined, "src="+cfg.Workspace+",") {
		t.Fatal("the original project folder was mounted into the container")
	}
	// Nothing of the operator's environment reaches the container process.
	if env := strings.Join(dockerEnvironment(), " "); strings.Contains(env, "private-key") {
		t.Fatal("a host credential reached the container runtime's environment")
	}
}

func TestProtocolScopeAuthenticationAndIdempotency(t *testing.T) {
	b, _ := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
	defer b.Close()
	r := httptest.NewRequest("GET", "http://broker.test/runs", nil)
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("anonymous broker access")
	}
	in := startRequest()
	first := request(t, b, "/runs", in.DispatchKey, in)
	again := request(t, b, "/runs", in.DispatchKey, in)
	if first.Code != 201 || again.Code != 200 || first.Body.String() != again.Body.String() {
		t.Fatal("start retry is not stable")
	}
	in.Task = "Different work"
	if request(t, b, "/runs", in.DispatchKey, in).Code != 409 {
		t.Fatal("key reuse accepted")
	}
	in = startRequest()
	in.Role = "manager"
	if request(t, b, "/runs", in.DispatchKey, in).Code != 403 {
		t.Fatal("manager accepted by direct-worker broker")
	}
	in = startRequest()
	in.ProjectID = "other-project"
	if request(t, b, "/runs", in.DispatchKey, in).Code != 403 {
		t.Fatal("cross-project work accepted")
	}
}
func TestInterruptedMessageCannotBypassExplicitResume(t *testing.T) {
	b, _ := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
	defer b.Close()
	w := request(t, b, "/runs", "dispatch-one", startRequest())
	var run worker.Run
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	_ = b.update(run.ID, func(r *storedRun) error { r.Run.Status = "interrupted"; return nil })
	if request(t, b, "/runs/"+run.ID+"/messages", "message-one", map[string]string{"message": "Continue"}).Code != 409 {
		t.Fatal("message bypassed recovery allowance")
	}
	if request(t, b, "/runs/"+run.ID+"/resume", "resume-one", map[string]string{"instruction": "Continue"}).Code != 200 {
		t.Fatal("explicit resume rejected")
	}
}
func TestWorkspaceFilterAndConfinedReads(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", source, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("init fixture: %s %v", out, err)
	}
	_ = os.WriteFile(filepath.Join(source, ".env"), []byte("private"), 0600)
	_ = os.WriteFile(filepath.Join(source, ".git", "hooks", "pre-commit"), []byte("private"), 0600)
	_ = os.WriteFile(filepath.Join(source, "normal.txt"), []byte("safe"), 0644)
	outside := filepath.Join(dir, "outside")
	_ = os.WriteFile(outside, []byte("private"), 0600)
	_ = os.Symlink(outside, filepath.Join(source, "link"))
	baseline, err := copyWorkspace(source, filepath.Join(dir, "copy"))
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 1 || string(baseline["normal.txt"]) != "safe" {
		t.Fatalf("unsafe copy: %v", baseline)
	}
	root, _ := os.OpenRoot(source)
	defer root.Close()
	if _, err = readConfined(root, "link"); err == nil {
		t.Fatal("followed outside symlink")
	}
}
func TestBinaryBaselineSurvivesPersistence(t *testing.T) {
	b, cfg := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
	defer b.Close()
	raw := []byte{0x89, 0x50, 0x4e, 0x47, 0xff, 0, 0xfe}
	dir := filepath.Join(cfg.StateDir, "runs", "binary", "workspace")
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "image.png"), raw, 0644)
	run := storedRun{Run: worker.Run{ID: "binary"}, WorkDir: dir, Baseline: map[string][]byte{"image.png": raw}}
	encoded, _ := json.Marshal(run)
	var restored storedRun
	_ = json.Unmarshal(encoded, &restored)
	if !reflect.DeepEqual(restored.Baseline["image.png"], raw) {
		t.Fatal("binary baseline corrupted")
	}
	if _, err := b.artifacts(restored); err != nil {
		t.Fatal(err)
	}
	patch, _ := os.ReadFile(filepath.Join(cfg.StateDir, "runs", "binary", "artifacts", "changes.patch"))
	if len(patch) != 0 {
		t.Fatalf("false binary diff after roundtrip: %s", patch)
	}
}

// The harness owns the conversation now, so there is no transcript to repair.
// What recovery has to get right on the daemon's side is in delivery_test.go;
// what belongs here is the ordinary path: the opening turn carries the
// assignment, and a confirmed delivery is neither repeated nor invented.
func TestTheOpeningTurnCarriesTheAssignmentAndIsNotRepeated(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	withDirection(t, b, id, "Prefer the smaller change")
	first, err := b.nextTurnInput(id)
	if err != nil || !strings.Contains(first, "Prefer the smaller change") || !strings.Contains(first, "Begin this assignment") {
		t.Fatalf("the opening turn did not carry the assignment and the direction: %q %v", first, err)
	}
	taken, _ := b.snapshot(id)
	if taken.InFlight == nil || len(taken.Messages) != 0 || taken.Briefed {
		t.Fatalf("direction was dequeued or the brief recorded before delivery: %+v", taken)
	}
	b.deliveryAccepted(id)
	delivered, _ := b.snapshot(id)
	if delivered.InFlight != nil || !delivered.Briefed {
		t.Fatalf("confirmed delivery was not recorded: %+v", delivered)
	}
	// With nothing queued there is genuinely nothing to say, and nothing is
	// invented to fill the turn.
	if next, err := b.nextTurnInput(id); err != nil || next != "" {
		t.Fatalf("a prompt was invented for an idle turn: %q %v", next, err)
	}
}

func TestDockerEnvironmentExcludesHostCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "private-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "private-cloud")
	t.Setenv("HOME", "/private-owner-home")
	got := dockerEnvironment()
	if len(got) != 2 || strings.Contains(strings.Join(got, "\n"), "private") {
		t.Fatalf("host credentials inherited: %v", got)
	}
}
func TestReviewWorkspaceIsReadOnly(t *testing.T) {
	b, _ := newFixture(t, "http://127.0.0.1:1", &fakeDocker{})
	defer b.Close()
	r := storedRun{Request: worker.StartRequest{Capabilities: []string{"review"}}, WorkDir: "/synthetic/workspace"}
	args := b.containerArgs(r)
	if !contains(args, "type=bind,src=/synthetic/workspace,dst=/workspace,readonly") {
		t.Fatal("review-only worker received writable source mount")
	}
}
func TestCleanupRequiresProvenContainerIdentity(t *testing.T) {
	r := storedRun{Run: worker.Run{ID: "fixture"}, Container: "agent-assistant-fixture"}
	for _, tc := range []struct {
		name       string
		inspect    []byte
		inspectErr error
		listingErr error
		wantErr    bool
	}{{"missing", nil, errors.New("missing"), nil, false}, {"unavailable", nil, errors.New("daemon down"), errors.New("daemon down"), true}, {"wrong-owner", []byte(strings.Repeat("a", 64) + " other"), nil, nil, true}, {"owned", []byte(strings.Repeat("a", 64) + " fixture"), nil, nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			removed := false
			cmd := CommandFunc(func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if args[0] == "rm" {
					removed = true
					if args[2] != strings.Repeat("a", 64) {
						t.Fatal("cleanup used mutable container name")
					}
					return nil, nil
				}
				if args[1] == "inspect" {
					return tc.inspect, tc.inspectErr
				}
				return nil, tc.listingErr
			})
			err := reconcileContainer(context.Background(), cmd, r)
			if (err != nil) != tc.wantErr {
				t.Fatalf("cleanup result %v", err)
			}
			if tc.wantErr && removed {
				t.Fatal("removed unverified container")
			}
		})
	}
}

func TestEvidenceDigestIsBoundedAndExplicitAboutOmissions(t *testing.T) {
	names := make([]string, 100)
	for i := range names {
		names[i] = strings.Repeat("x", 2000)
	}
	commands := make([]commandRecord, 30)
	for i := range commands {
		commands[i] = commandRecord{Command: strings.Repeat("c", 8000), Success: i > 0, Output: strings.Repeat("o", 64*1024)}
	}
	evidence := strings.Join(evidenceDigest(names, commands, strings.Repeat("patch\n", 10000)), "\n")
	if len(evidence) > 20000 {
		t.Fatalf("evidence grew beyond context bound: %d", len(evidence))
	}
	for _, want := range []string{"29 SUCCEEDED, 1 FAILED", "Command 1 FAILED", "84 omitted", "24 omitted", "TRUNCATED:", "additional command output may be omitted"} {
		if !strings.Contains(evidence, want) {
			t.Errorf("missing explicit evidence limitation %q", want)
		}
	}
}
