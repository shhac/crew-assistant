package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// handlerTransport answers requests with handler in memory. Tests use it
// instead of a listening server: QA's sandbox refuses to bind any socket.
type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec.Result(), nil
}

// standIn is a CLI whose daemon is handler, with the runtime files a running
// daemon leaves and token as its admin token.
func standIn(t *testing.T, handler http.Handler, token string) *options {
	t.Helper()
	o := &options{statePath: filepath.Join(t.TempDir(), "state.db"), transport: handlerTransport{handler}}
	if err := os.MkdirAll(o.runtimeDir(), 0700); err != nil {
		t.Fatal(err)
	}
	const url = "http://daemon.invalid"
	info, err := json.Marshal(runtimeInfo{URL: url, LocalURL: url})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.runtimeDir(), "daemon.json"), info, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.runtimeDir(), "admin-token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	return o
}
