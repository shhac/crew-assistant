// Package testutil holds helpers shared by the module's tests.
package testutil

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"syscall"
	"testing"
)

// NewServer starts a local test server, or skips the test where listening on
// loopback is forbidden in an ordinary role session. A daemon-hosted required
// check must execute it; denied capabilities fail rather than silently pass.
func NewServer(t testing.TB, handler http.Handler) *httptest.Server {
	t.Helper()
	listener := listen(t)
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	return server
}

// NewModelServer is NewServer for a fake OpenAI-compatible endpoint: its
// replies are JSON unless the handler says otherwise, as a real endpoint's
// are, and as the harness requires.
func NewModelServer(t testing.TB, handler http.Handler) *httptest.Server {
	t.Helper()
	return NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler.ServeHTTP(w, r)
	}))
}

// RequireLoopback skips a test that listens on loopback itself, such as one
// that starts the daemon, where listening is forbidden.
func RequireLoopback(t testing.TB) {
	t.Helper()
	listen(t).Close()
}

func listen(t testing.TB) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			SkipRequired(t, "loopback", err.Error())
		}
		t.Fatal(err)
	}
	return listener
}

// SkipRequired identifies coverage that must execute in the hosted check.
func SkipRequired(t testing.TB, capability, reason string) {
	t.Helper()
	if os.Getenv("CREW_HOSTED_CHECK") == "1" {
		t.Fatalf("required hosted-check capability %s unavailable: %s", capability, reason)
	}
	t.Skipf("required capability %s unavailable: %s", capability, reason)
}

func ListenLoopback(t testing.TB) net.Listener { t.Helper(); return listen(t) }

// RequireSymlinkResult permits only Windows' explicit missing symlink privilege.
// Invalid fixture paths, exhausted disks and other creation errors are failures.
func RequireSymlinkResult(t testing.TB, err error) {
	t.Helper()
	requireSymlinkResult(t, err, runtime.GOOS)
}

func requireSymlinkResult(t testing.TB, err error, platform string) {
	t.Helper()
	if err == nil {
		return
	}
	link, ok := err.(*os.LinkError)
	if platform == "windows" && ok && link.Op == "symlink" && link.Err == syscall.Errno(1314) {
		t.Skipf("unsupported capability: Windows symlink privilege (ERROR_PRIVILEGE_NOT_HELD): %v", err)
	}
	t.Fatalf("create symlink fixture: %v", err)
}
