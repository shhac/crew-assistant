// Package testutil holds helpers shared by the module's tests.
package testutil

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
)

// NewServer starts a local test server, or skips the test where listening on
// loopback is forbidden, as it is inside a team role's sandbox. Those tests
// still run everywhere else, including CI.
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
			t.Skip("loopback networking is not permitted here, as inside a sandboxed check; this test runs outside it")
		}
		t.Fatal(err)
	}
	return listener
}
