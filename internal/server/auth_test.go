package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	code, _ := Pair(dir)
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
