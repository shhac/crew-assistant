package work

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/statepath"
)

// usageStore is display-only; admission still reads the meter.
type usageStore struct {
	mu      sync.Mutex
	loaded  bool
	dirty   bool
	entries map[string]keptUsage
}
type keptUsage struct {
	Bin       string
	Home      string
	At        time.Time
	Remaining quota.Remaining
}

func (lp *Loop) keepUsage(engine string, h config.Harness, r quota.Reading, out quota.Remaining) quota.Remaining {
	if lp.Demo {
		return out
	}
	s := &lp.keptUsage
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(lp.Core.StateDirectory(), "usage.json")
	if !s.loaded {
		s.loaded = true
		if data, err := os.ReadFile(path); err == nil {
			var entries map[string]keptUsage
			if json.Unmarshal(data, &entries) == nil {
				s.entries = entries
			}
		}
		if s.entries == nil {
			s.entries = make(map[string]keptUsage)
		}
	}
	last, ok := s.entries[engine]
	same := ok && last.Bin == h.Bin && last.Home == h.Home
	if len(out.Windows) > 0 || out.Credits != nil {
		if !r.At.IsZero() && (!same || r.At.After(last.At)) {
			s.entries[engine] = keptUsage{Bin: h.Bin, Home: h.Home, At: r.At, Remaining: out}
			s.dirty = true
		}
		if !s.dirty {
			return out
		}
		// A cached good read also retries a failed write.
		data, err := json.Marshal(s.entries)
		if err == nil {
			err = statepath.WriteFileAtomic(path, data)
		}
		if err != nil {
			lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "usage_save"}, err)
		} else {
			s.dirty = false
		}
		return out
	}
	if !same || last.At.IsZero() || out.Missing == "not installed" || out.Missing == "not signed in" {
		return out
	}
	kept := last.Remaining
	kept.Windows = append([]quota.Window(nil), kept.Windows...)
	kept.AsOf = &last.At
	kept.Missing = out.Missing
	return kept
}
