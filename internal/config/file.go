package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/shhac/lib-agent-cli/creds"
)

// Load reads the config file, in the current layout or an earlier one. Keys
// it doesn't know are kept in the file and reported by UnknownKeys, never an
// error: a file written by a newer version still loads.
func Load(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if data, err = ConvertLegacyJSON(data); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = dec.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return c, errors.New("config must contain one JSON object")
	}
	c.Assistant.Theme = NormalizeTheme(c.Assistant.Theme)
	return c, c.Validate()
}

// Save writes c over the config file, keeping what it doesn't model: notes
// and keys from a newer version. It holds the file's lock, so the daemon, the
// CLI and the dashboard never write over each other, and it finishes moving
// a file in an earlier layout to this one first, so no earlier key outlives
// the save.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s := Document(path)
	return s.WithLock(func() error {
		if _, err := upgradeLocked(path); err != nil {
			return err
		}
		return s.Save(c)
	})
}

// Upgrade rewrites a config file in an earlier layout into the current one,
// once, under the file's lock, keeping everything else in it. It reports
// whether it rewrote anything.
func Upgrade(path string) (bool, error) {
	changed := false
	err := Document(path).WithLock(func() (err error) {
		changed, err = upgradeLocked(path)
		return err
	})
	return changed, err
}

func upgradeLocked(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc, changed, err := convertDocument(data)
	if err != nil {
		return false, fmt.Errorf("decode config: %w", err)
	}
	if !changed {
		return false, nil
	}
	// The whole document is written as converted: laid over the stored one,
	// the keys it moved would come back.
	return true, creds.Store{Path: path}.Save(doc)
}

// UnknownKeys are the keys in the config file the current layout doesn't
// have, each with where it moved when it was renamed.
func UnknownKeys(path string) []UnknownKey {
	var out []UnknownKey
	for _, k := range Document(path).UnknownKeys(Config{}) {
		to, renamed := RenamedKey(k.Path)
		out = append(out, UnknownKey{Path: k.Path, Value: k.Value, Renamed: renamed, To: to})
	}
	return out
}

// UnknownKey is a key the config file holds that the current layout doesn't.
type UnknownKey struct {
	Path, Value string
	// Renamed says it is an earlier layout's key; To is where it went, or
	// "" when it was dropped.
	Renamed bool
	To      string
}

// String says what became of the key, for the owner to act on.
func (k UnknownKey) String() string {
	switch {
	case k.Renamed && k.To == "":
		return k.Path + " is no longer a setting; remove it with crew-assistant config unset " + k.Path
	case k.Renamed:
		return k.Path + " is now " + k.To
	}
	return k.Path + " is not a setting this version knows, so it has no effect; remove it with crew-assistant config unset " + k.Path
}

// Document is the config file as a document, for reading and removing keys
// by path, including ones this version doesn't know.
func Document(path string) creds.Store { return creds.Store{Path: path, Overlay: true} }
