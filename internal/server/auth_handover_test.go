package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestAuthHandoverPreservesCookieAndExpiryOnce(t *testing.T) {
	dir := t.TempDir()
	old, err := NewAuth(dir, "http://localhost:1234", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	old.sessions["paired-browser"] = expiry
	old.sessions["expired"] = time.Now().Add(-time.Hour)
	if err = old.SaveHandover(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "upgrade-sessions.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	newAuth, err := NewAuth(dir, "http://localhost:1234", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !newAuth.sessions["paired-browser"].Equal(expiry) || newAuth.admin == old.admin {
		t.Fatal("expiry changed or admin was not rotated")
	}
	for _, tc := range []struct {
		token string
		want  bool
	}{{"paired-browser", true}, {"expired", false}} {
		r := httptest.NewRequest("GET", "http://localhost:1234/api/state", nil)
		r.AddCookie(&http.Cookie{Name: config.Namespace + ".session", Value: tc.token})
		if newAuth.recognized(r) != tc.want {
			t.Fatal(tc)
		}
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("recovery handover lost", err)
	}
	third, err := NewAuth(dir, "http://localhost:1234", "", nil)
	if err != nil || !third.sessions["paired-browser"].Equal(expiry) {
		t.Fatal("handover reused", err)
	}
}

func TestInvalidAuthHandoverDoesNotLeakCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "upgrade-sessions.json"), []byte("credential-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewAuth(dir, "http://localhost:1234", "", nil)
	if err == nil || err.Error() != "could not read browser sessions from upgrade handover" {
		t.Fatal(err)
	}
}
