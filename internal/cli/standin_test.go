package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/server"
)

// handlerTransport answers requests with handler in memory, as they would
// arrive through the owner's socket. Tests use it instead of a listening
// server: QA's sandbox refuses to bind any socket.
type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r.WithContext(server.AsOwner(r.Context())))
	return rec.Result(), nil
}

// standIn is a CLI whose daemon is handler, with the runtime record a running
// daemon leaves.
func standIn(t *testing.T, handler http.Handler) *options {
	t.Helper()
	o := &options{statePath: filepath.Join(t.TempDir(), "state.db"), transport: handlerTransport{handler}}
	if err := os.MkdirAll(o.runtimeDir(), 0700); err != nil {
		t.Fatal(err)
	}
	const url = "http://daemon.invalid"
	info, err := json.Marshal(runtimeInfo{URL: url, LocalURL: url, Socket: "stand-in.sock"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.runtimeDir(), "daemon.json"), info, 0600); err != nil {
		t.Fatal(err)
	}
	return o
}
