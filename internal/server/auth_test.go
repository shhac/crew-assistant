package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestPrivateAPIAndPairing(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	request := func(path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		method := "GET"
		if body != "" {
			method = "POST"
		}
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Requested-With", "crew-assistant")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if request("/api/state", "", "", nil).Code != 401 {
		t.Fatal("anonymous read allowed")
	}
	code := a.Pair()
	body := `{"token":"` + code + `"}`
	if request("/api/session", body, "http://evil.example", nil).Code != 403 {
		t.Fatal("cross origin login")
	}
	login := request("/api/session", body, "", nil)
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	if request("/api/session", body, "", nil).Code != 401 {
		t.Fatal("code reused")
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("session cookie missing")
	}
	if request("/api/state", "", "", cookies[0]).Code != 204 {
		t.Fatal("session failed")
	}
}
func TestTailscaleOnlyRecognizedLocalIdentity(t *testing.T) {
	a, _ := NewAuth(t.TempDir(), "http://127.0.0.1:8340", "https://host.example:8443", []string{"owner@example.test"})
	for _, tc := range []struct {
		peer, user string
		want       bool
	}{{"127.0.0.1:1234", "owner@example.test", true}, {"192.0.2.1:1", "owner@example.test", false}, {"127.0.0.1:1234", "guest@example.test", false}, {"127.0.0.1:1234", "", false}} {
		r := httptest.NewRequest("GET", "https://host.example:8443/api/state", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("Tailscale-User-Login", tc.user)
		if a.recognized(r) != tc.want {
			t.Fatalf("peer %s user %s", tc.peer, tc.user)
		}
	}
}

func TestDecodeRejectsTrailingMalformedOrOversizedJSON(t *testing.T) {
	for _, body := range []string{`{"token":"valid"} {`, `{"token":"valid"} garbage`, `{"token":"valid"} {"extra":true}`, `{"token":"valid"}` + strings.Repeat(" ", 1<<20)} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8340/api/session", strings.NewReader(body))
		w := httptest.NewRecorder()
		var v struct {
			Token string `json:"token"`
		}
		if err := decode(w, r, &v); err == nil || w.Code != 400 {
			t.Errorf("accepted malformed trailing data: error=%v status=%d", err, w.Code)
		}
	}
}
func TestDecodeAcceptsOneObjectAndWhitespace(t *testing.T) {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8340/api/session", strings.NewReader("{\"token\":\"valid\"}\n\t "))
	w := httptest.NewRecorder()
	var v struct {
		Token string `json:"token"`
	}
	if err := decode(w, r, &v); err != nil || v.Token != "valid" {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}

func TestAuthRefusesWhatIsNotTheOwnersOwnDashboard(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	send := func(method, host, remote, path, body string, headers map[string]string, cookie *http.Cookie) int {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		r.RemoteAddr = remote
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	dashboard := map[string]string{"X-Requested-With": "crew-assistant"}
	if send("GET", "evil.example:8340", "127.0.0.1:1", "/api/state", "", nil, nil) != 403 {
		t.Error("a request for another host name was served (DNS rebinding)")
	}
	// Loopback is open to a role given localhost, so no header from it is a credential.
	if send("GET", "127.0.0.1:8340", "127.0.0.1:1", "/api/state", "", map[string]string{"Authorization": "Bearer anything"}, nil) != 401 {
		t.Error("a bearer token from loopback was accepted")
	}
	if send("POST", "127.0.0.1:8340", "127.0.0.1:1", "/api/pairing", "", dashboard, nil) != 401 {
		t.Error("a sign-in code was issued to loopback")
	}
	code := a.Pair()
	if send("POST", "127.0.0.1:8340", "127.0.0.1:1", "/api/session", `{"token":"`+code+`"}`, nil, nil) != 403 {
		t.Error("a write without the dashboard's header was accepted")
	}
	a.pairedAt = time.Now().Add(-6 * time.Minute)
	if send("POST", "127.0.0.1:8340", "127.0.0.1:1", "/api/session", `{"token":"`+code+`"}`, dashboard, nil) != 401 {
		t.Error("a sign-in code older than five minutes was accepted")
	}
	a.sessions["stale"] = time.Now().Add(-time.Minute)
	if send("GET", "127.0.0.1:8340", "127.0.0.1:1", "/api/state", "", nil, &http.Cookie{Name: config.Namespace + ".session", Value: "stale"}) != 401 {
		t.Error("an expired session was accepted")
	}
}

// Every role can read the owner's files, so none of them is a credential:
// NewAuth tightens the runtime directory and removes what earlier versions
// left there.
func TestNewAuthTightensTheRuntimeDirectoryAndRemovesOldCredentials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"admin-token", "pairing-code"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stale"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("the runtime directory: %v %v", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("credentials left in the runtime directory: %v %v", entries, err)
	}
}

// fromTheOwnersSocket is r as it arrives through OwnerListener.
func fromTheOwnersSocket(r *http.Request) *http.Request {
	return r.WithContext(AsOwner(r.Context()))
}

// The owner's commands reach the daemon over a Unix socket, which is the only
// way to be recognised without a paired browser, and the only way to get a
// sign-in code.
func TestOnlyTheOwnersSocketIsRecognisedWithoutPairing(t *testing.T) {
	dir, err := os.MkdirTemp("", "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	a, err := NewAuth(filepath.Join(dir, "runtime"), "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "owner.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip("this environment refuses a Unix socket:", err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("this environment refuses a loopback listener:", err)
	}
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	srv := &http.Server{Handler: h, ConnContext: ConnContext}
	go srv.Serve(OwnerListener(l))
	go srv.Serve(tcp)
	t.Cleanup(func() { srv.Close() })
	viaSocket := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	get := func(c *http.Client, method, url string) *http.Response {
		r, err := http.NewRequest(method, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Requested-With", "crew-assistant")
		resp, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if resp := get(viaSocket, "GET", "http://crew-assistant/api/state"); resp.StatusCode != 204 {
		t.Fatalf("the owner's socket was refused: %d", resp.StatusCode)
	}
	local := "http://127.0.0.1:8340"
	byTCP := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", tcp.Addr().String())
	}}}
	if resp := get(byTCP, "GET", local+"/api/state"); resp.StatusCode != 401 {
		t.Fatalf("loopback TCP was recognised without pairing: %d", resp.StatusCode)
	}
	if resp := get(byTCP, "POST", local+"/api/pairing"); resp.StatusCode != 401 {
		t.Fatalf("loopback TCP was given a sign-in code: %d", resp.StatusCode)
	}
	resp := get(viaSocket, "POST", "http://crew-assistant/api/pairing")
	var pairing struct{ Code string }
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&pairing) != nil || pairing.Code == "" || pairing.Code != a.pairing {
		t.Fatalf("the owner's socket got no sign-in code: %d", resp.StatusCode)
	}
}

// Tailscale Serve ends HTTPS and proxies plain HTTP from this machine, so
// only the public host can tell the session cookie must be Secure.
func TestTheSessionCookieIsSecureOnlyThroughThePublicHost(t *testing.T) {
	for _, tc := range []struct {
		name, local, public, host string
		secure                    bool
	}{
		{"loopback without Tailscale", "http://127.0.0.1:8340", "", "127.0.0.1:8340", false},
		{"loopback with Tailscale", "http://127.0.0.1:8340", "https://host.example:8443", "127.0.0.1:8340", false},
		{"IPv6 loopback with Tailscale", "http://[::1]:8340", "https://host.example:8443", "[::1]:8340", false},
		{"public host", "http://127.0.0.1:8340", "https://host.example:8443", "host.example:8443", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a, err := NewAuth(dir, tc.local, tc.public, []string{"owner@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			code := a.Pair()
			r := httptest.NewRequest("POST", "http://"+tc.host+"/api/session", strings.NewReader(`{"token":"`+code+`"}`))
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("X-Requested-With", "crew-assistant")
			w := httptest.NewRecorder()
			a.Middleware(http.NotFoundHandler()).ServeHTTP(w, r)
			cookies := w.Result().Cookies()
			if w.Code != 200 || len(cookies) != 1 {
				t.Fatal(w.Code, w.Body.String())
			}
			if cookies[0].Secure != tc.secure {
				t.Errorf("Secure = %v, want %v", cookies[0].Secure, tc.secure)
			}
		})
	}
}

// Without Tailscale nothing proxies the dashboard, so the identity header is
// whatever a local process chose to send.
func TestATailscaleIdentityIsIgnoredWhenTailscaleIsOff(t *testing.T) {
	a, err := NewAuth(t.TempDir(), "http://127.0.0.1:8340", "", []string{"owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:8340/api/state", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Tailscale-User-Login", "owner@example.test")
	w := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("a spoofed Tailscale login was let in: %d", w.Code)
	}
}
