package server

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

type Auth struct {
	mu           sync.Mutex
	admin        string
	pairingPath  string
	sessions     map[string]time.Time
	allowedHosts map[string]bool
	tailscale    bool
	users        []string
}

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("system entropy unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func NewAuth(dir, localURL, publicURL string, users []string) (*Auth, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	a := &Auth{admin: secret(), pairingPath: filepath.Join(dir, "pairing-code"), sessions: map[string]time.Time{}, allowedHosts: map[string]bool{}, tailscale: publicURL != "", users: users}
	for _, raw := range []string{localURL, publicURL} {
		u, err := url.Parse(raw)
		if err == nil && u.Host != "" {
			a.allowedHosts[u.Host] = true
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "admin-token"), []byte(a.admin), 0600); err != nil {
		return nil, err
	}
	return a, nil
}
func Pair(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	code := secret()
	p := filepath.Join(dir, "pairing-code")
	f, err := os.CreateTemp(dir, ".pairing-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(code); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(f.Name(), p); err != nil {
		return "", err
	}
	return code, nil
}
func equal(a, b string) bool {
	return len(a) > 0 && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
func (a *Auth) recognized(r *http.Request) bool {
	if isLoopback(r) && equal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), a.admin) {
		return true
	}
	if a.tailscale && isLoopback(r) {
		for _, u := range a.users {
			if u != "" && strings.EqualFold(r.Header.Get("Tailscale-User-Login"), u) {
				return true
			}
		}
	}
	cookie, err := r.Cookie(config.Namespace + ".session")
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expiry, ok := a.sessions[cookie.Value]
	return ok && time.Now().Before(expiry)
}
func (a *Auth) sameOrigin(r *http.Request) bool {
	if !a.allowedHosts[r.Host] {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	} // CLI requests have no Origin and still require credentials.
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if !a.allowedHosts[r.Host] {
			fail(w, 403, "unrecognized dashboard host")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != "GET" && r.Method != "HEAD" {
				if !a.sameOrigin(r) || r.Header.Get("X-Requested-With") != "crew-assistant" {
					fail(w, 403, "request must come from this dashboard")
					return
				}
			}
			if r.URL.Path == "/api/session" && r.Method == "POST" {
				a.login(w, r)
				return
			}
			if !a.recognized(r) {
				fail(w, 401, "This browser isn't paired. Run crew-assistant dashboard open --print for a pairing code.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Token string `json:"token"`
	}
	if decode(w, r, &v) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st, err := os.Stat(a.pairingPath)
	b, readErr := os.ReadFile(a.pairingPath)
	if err != nil || readErr != nil || time.Since(st.ModTime()) > 5*time.Minute || !equal(v.Token, string(b)) {
		fail(w, 401, "That pairing code is wrong or has expired.")
		return
	}
	if err := os.Remove(a.pairingPath); err != nil {
		fail(w, 500, "Couldn't check the pairing code. Try again.")
		return
	}
	for token, expires := range a.sessions {
		if time.Now().After(expires) {
			delete(a.sessions, token)
		}
	}
	token := secret()
	a.sessions[token] = time.Now().Add(12 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: config.Namespace + ".session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || (a.tailscale && isLoopback(r) && strings.HasPrefix(r.Host, "127.") == false && r.Host != "localhost"), MaxAge: 43200})
	respond(w, 200, map[string]bool{"ok": true})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "invalid request body")
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, "request must contain one JSON object")
		return errors.New("trailing JSON")
	}
	return nil
}

// decodeConfig reads a config in any layout: a dashboard left open across an
// upgrade still sends the one it loaded.
func decodeConfig(w http.ResponseWriter, r *http.Request, c *config.Config) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err == nil {
		data, err = config.ConvertLegacyJSON(data)
	}
	if err != nil {
		fail(w, 400, "invalid request body")
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return decode(w, r, c)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
