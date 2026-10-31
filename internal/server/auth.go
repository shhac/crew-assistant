package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/statepath"
)

type Auth struct {
	mu sync.Mutex
	// pairing is the one-use sign-in code, held only in memory: a role can read
	// the owner's files, so a code on disk would be a way in.
	pairing      string
	pairedAt     time.Time
	sessions     map[string]time.Time
	allowedHosts map[string]bool
	tailscale    bool
	publicHost   string
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
	// MkdirAll keeps the mode of a directory that already exists.
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	a := &Auth{sessions: map[string]time.Time{}, allowedHosts: map[string]bool{}, tailscale: publicURL != "", users: users}
	for _, raw := range []string{localURL, publicURL} {
		u, err := url.Parse(raw)
		if err == nil && u.Host != "" {
			a.allowedHosts[u.Host] = true
		}
	}
	if u, err := url.Parse(publicURL); err == nil {
		a.publicHost = u.Host
	}
	// Earlier versions kept an admin token and pairing code here, readable by
	// any role; the owner's channel is now the private socket.
	for _, name := range []string{"admin-token", "pairing-code"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if err := a.loadHandover(dir); err != nil {
		return nil, err
	}
	return a, nil
}

// SaveHandover snapshots sessions before installation and refreshes them after HTTP drains.
// Cookies retain their original expiry; a pairing code does not carry over.
func (a *Auth) SaveHandover(dir string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessions := make(map[string]time.Time)
	for token, expiry := range a.sessions {
		if time.Now().Before(expiry) {
			sessions[token] = expiry
		}
	}
	b, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	if err := statepath.WriteFileAtomic(filepath.Join(dir, "upgrade-sessions.json"), b); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (a *Auth) loadHandover(dir string) error {
	path := filepath.Join(dir, "upgrade-sessions.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var sessions map[string]time.Time
	if err = json.Unmarshal(b, &sessions); err != nil {
		// Do not echo file contents, which are authentication credentials.
		return errors.New("could not read browser sessions from upgrade handover")
	}
	for token, expiry := range sessions {
		if time.Now().Before(expiry) {
			a.sessions[token] = expiry
		}
	}
	return nil
}

// Pair issues a one-use sign-in code for a browser, replacing any earlier one.
func (a *Auth) Pair() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pairing, a.pairedAt = secret(), time.Now()
	return a.pairing
}

// ownerKey marks a request that came through the owner's private socket.
type ownerKey struct{}

type ownerConn struct{ net.Conn }

type ownerListener struct{ net.Listener }

func (l ownerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return ownerConn{c}, nil
}

// OwnerListener marks every connection from l as the owner's. l must be a Unix
// socket in a directory only the owner can open: role sandboxes refuse
// Unix-domain sockets, while loopback TCP is open to a role given localhost.
func OwnerListener(l net.Listener) net.Listener { return ownerListener{l} }

// ConnContext is the http.Server hook that carries OwnerListener's mark into
// each request.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if _, ok := c.(ownerConn); ok {
		return AsOwner(ctx)
	}
	return ctx
}

// AsOwner marks ctx as the owner's own request, for a caller in this process.
func AsOwner(ctx context.Context) context.Context { return context.WithValue(ctx, ownerKey{}, true) }

func fromOwner(r *http.Request) bool { owner, _ := r.Context().Value(ownerKey{}).(bool); return owner }

func equal(a, b string) bool {
	return len(a) > 0 && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
func (a *Auth) recognized(r *http.Request) bool {
	if fromOwner(r) {
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
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if fromOwner(r) {
			// No browser reaches a Unix socket, so host and origin checks don't
			// apply; writes still carry the header every caller of the API sends.
			if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Requested-With") != "crew-assistant" {
				fail(w, 403, "request must come from this dashboard")
				return
			}
			if r.URL.Path == "/api/pairing" && r.Method == "POST" {
				respond(w, 200, map[string]string{"code": a.Pair(), "expires": "5 minutes"})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !a.allowedHosts[r.Host] {
			fail(w, 403, "unrecognized dashboard host")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
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
	if time.Since(a.pairedAt) > 5*time.Minute || !equal(v.Token, a.pairing) {
		fail(w, 401, "That pairing code is wrong or has expired.")
		return
	}
	a.pairing = ""
	for token, expires := range a.sessions {
		if time.Now().After(expires) {
			delete(a.sessions, token)
		}
	}
	token := secret()
	a.sessions[token] = time.Now().Add(12 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: config.Namespace + ".session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || (a.publicHost != "" && r.Host == a.publicHost), MaxAge: 43200})
	respond(w, 200, map[string]bool{"ok": true})
}
