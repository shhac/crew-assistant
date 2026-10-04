// Package upgrade checks installable releases and provides self-upgrade recovery.
package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/releaseversion"
)

const FastInterval = 2 * time.Minute
const FastWindow = time.Hour
const NotesLimit = 16 * 1024
const bodyLimit = 1024 * 1024

type Result struct {
	Available, Notes, URL, Error string
	CheckedAt                    time.Time
}
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type realClock struct{}

func SystemClock() Clock { return realClock{} }

type realTimer struct{ *time.Timer }

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
func (t realTimer) C() <-chan time.Time          { return t.Timer.C }

type Checker struct {
	version string
	client  HTTPClient
	clock   Clock
	checkMu sync.Mutex // Includes recording, so concurrent checks stay ordered.
	mu      sync.Mutex
	target  string
	until   time.Time
	wake    chan struct{}
}

func New(version string, client HTTPClient, clock Clock) *Checker {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if clock == nil {
		clock = realClock{}
	}
	return &Checker{version: version, client: client, clock: clock, wake: make(chan struct{}, 1)}
}
func Unavailable(version string, demo bool) string {
	if demo {
		return "Updates aren't checked in demo mode."
	}
	if !releaseversion.Valid(version) {
		return "Updates aren't checked for a development build."
	}
	return ""
}
func (c *Checker) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *Checker) Nudge(version string) {
	if !releaseversion.Valid(version) || !releaseversion.Valid(c.version) || releaseversion.Compare(version, c.version) <= 0 {
		return
	}
	c.mu.Lock()
	if c.target == "" || releaseversion.Compare(version, c.target) > 0 {
		c.target, c.until = version, c.clock.Now().Add(FastWindow)
	}
	c.mu.Unlock()
	c.Wake()
}
func (c *Checker) next(interval time.Duration, result Result) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	if c.target != "" && (!now.Before(c.until) || result.Error == "" && result.Available != "" && releaseversion.Compare(result.Available, c.target) >= 0) {
		c.target, c.until = "", time.Time{}
	}
	if c.target != "" {
		return min(FastInterval, c.until.Sub(now))
	}
	return interval
}

// Run checks at start, on config changes and release nudges, and periodically.
// Record runs under the check mutex, and never after cancellation.
// A failed record is reported and retried on the next timer or wake.
func (c *Checker) Run(ctx context.Context, cfg func() config.UpgradeSettings, record func(context.Context, Result) error, report func(error)) error {
	if Unavailable(c.version, false) != "" {
		return nil
	}
	for {
		c.checkMu.Lock()
		result, err := c.check(ctx, cfg())
		if err == nil && ctx.Err() == nil {
			err = record(ctx, result)
		}
		c.checkMu.Unlock()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && report != nil {
			report(err)
		}
		timer := c.clock.NewTimer(c.next(cfg().Interval(), result))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-c.wake:
			timer.Stop()
		case <-timer.C():
		}
	}
}
func (c *Checker) Check(ctx context.Context, cfg config.UpgradeSettings) (Result, error) {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	return c.check(ctx, cfg)
}
func (c *Checker) check(ctx context.Context, cfg config.UpgradeSettings) (Result, error) {
	if reason := Unavailable(c.version, false); reason != "" {
		return Result{}, errors.New(reason)
	}
	result := Result{CheckedAt: c.clock.Now().UTC()}
	found, err := c.fetch(ctx, cfg)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		result.Error = bounded(err.Error(), 512)
	} else {
		result.Available, result.Notes, result.URL = found.Available, found.Notes, found.URL
	}
	return result, nil
}

var explicitVersion = regexp.MustCompile(`(?m)^\s*version\s+["']([^"']+)["']`)
var taggedURL = regexp.MustCompile(`(?m)^\s*url\s+["'][^"']*/(?:tags/|download/)(v?[^/"']+)(?:/|["'])`)

func formulaVersion(body []byte) (string, error) {
	for _, re := range []*regexp.Regexp{explicitVersion, taggedURL} {
		if m := re.FindSubmatch(body); len(m) > 1 {
			v := strings.TrimSuffix(strings.TrimSuffix(string(m[1]), ".tar.gz"), ".zip")
			if releaseversion.Valid(v) {
				return "v" + strings.TrimPrefix(v, "v"), nil
			}
			return "", errors.New("formula has a malformed version")
		}
	}
	return "", errors.New("formula has no version tag")
}
func (c *Checker) fetch(ctx context.Context, cfg config.UpgradeSettings) (Result, error) {
	body, err := c.get(ctx, cfg.FormulaSourceURL())
	if err != nil {
		return Result{}, err
	}
	version, err := formulaVersion(body)
	if err != nil {
		return Result{}, err
	}
	if releaseversion.Compare(version, c.version) <= 0 {
		return Result{}, nil
	}
	endpoint := cfg.ReleaseURL()
	body, err = c.get(ctx, endpoint)
	if err != nil {
		return Result{}, err
	}
	var release struct {
		Tag   string `json:"tag_name"`
		Notes string `json:"body"`
		URL   string `json:"html_url"`
		Draft bool   `json:"draft"`
	}
	if err = json.Unmarshal(body, &release); err != nil {
		return Result{}, errors.New("release source returned malformed JSON")
	}
	// GitHub may be ahead of the formula: fetch this formula version's notes.
	if releaseversion.Valid(release.Tag) && releaseversion.Compare(release.Tag, version) != 0 && strings.HasSuffix(endpoint, "/latest") {
		body, err = c.get(ctx, strings.TrimSuffix(endpoint, "/latest")+"/tags/"+url.PathEscape(version))
		if err != nil {
			return Result{}, err
		}
		release.Notes, release.URL = "", ""
		if err = json.Unmarshal(body, &release); err != nil {
			return Result{}, errors.New("release source returned malformed JSON")
		}
	}
	if release.Draft || !releaseversion.Valid(release.Tag) || releaseversion.Compare(release.Tag, version) != 0 {
		return Result{}, errors.New("release source does not match the formula version")
	}
	link, err := url.Parse(release.URL)
	if err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil {
		return Result{}, errors.New("release source has an invalid release URL")
	}
	return Result{Available: version, Notes: bounded(release.Notes, NotesLimit), URL: release.URL}, nil
}
func (c *Checker) get(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid update source URL")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("update source could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update source returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
	if err != nil {
		return nil, errors.New("update source could not be read")
	}
	if len(body) > bodyLimit {
		return nil, errors.New("update source exceeded the size limit")
	}
	return body, nil
}
func bounded(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "") + "\n[truncated]"
}
