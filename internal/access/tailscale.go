// Package access owns optional private dashboard exposure.
package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shhac/lib-agent-mcp/tailscale"

	"github.com/shhac/crew-assistant/internal/procgroup"
)

type Runner func(context.Context, ...string) ([]byte, error)
type WireFunc func(context.Context, string, int, string, string) (string, func() error, error)
type Tailscale struct {
	Run  Runner
	Wire WireFunc
}
type ownership struct {
	Address string          `json:"address"`
	Port    int             `json:"port"`
	Route   json.RawMessage `json:"route"`
}

func DefaultTailscale() Tailscale {
	return Tailscale{
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "tailscale", args...)
			procgroup.Detach(cmd)
			return cmd.Output()
		},
		Wire: tailscale.Wire,
	}
}

// route extracts just the selected port, including any public exposure, leaving
// other applications' routes outside our ownership record.
func route(raw []byte, port int) (json.RawMessage, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, errors.New("invalid Tailscale Serve status")
	}
	selected := map[string]json.RawMessage{}
	for _, section := range []string{"TCP", "Web", "AllowFunnel"} {
		if len(cfg[section]) == 0 {
			continue
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(cfg[section], &entries); err != nil {
			return nil, err
		}
		for key, value := range entries {
			if key == strconv.Itoa(port) || strings.HasSuffix(key, ":"+strconv.Itoa(port)) {
				selected[section+"/"+key] = value
			}
		}
	}
	return json.Marshal(selected)
}
func (t Tailscale) current(ctx context.Context, port int) (json.RawMessage, error) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := t.Run(c, "serve", "status", "--json")
	if err != nil {
		return nil, errors.New("cannot inspect Tailscale Serve; check tailscale login and permissions")
	}
	return route(raw, port)
}

// Start refuses routes belonging to another installation. The caller must hold
// the state-directory instance lock and bind its local listener first.
func (t Tailscale) Start(ctx context.Context, port int, addr, record string) (string, func() error, error) {
	if port != 443 && port != 8443 && port != 10000 {
		return "", nil, errors.New("Tailscale port must be 443, 8443, or 10000")
	}
	current, err := t.current(ctx, port)
	if err != nil {
		return "", nil, err
	}
	if string(current) != "{}" {
		raw, err := os.ReadFile(record)
		var old ownership
		if err != nil || json.Unmarshal(raw, &old) != nil || old.Address != addr || old.Port != port || string(old.Route) != string(current) {
			return "", nil, fmt.Errorf("Tailscale HTTPS port %d already has an unowned or changed route; choose another port", port)
		}
	}
	op, cancel := context.WithTimeout(ctx, 15*time.Second)
	url, down, err := t.Wire(op, "serve", port, addr, "")
	cancel()
	if err != nil {
		return "", nil, errors.New("could not enable private Tailscale Serve; check HTTPS setup and tailscale status")
	}
	expected, err := t.current(ctx, port)
	if err != nil {
		return "", nil, fmt.Errorf("Serve started but ownership could not be recorded: %w", err)
	}
	if string(expected) == "{}" {
		return "", nil, errors.New("Serve started but no route appeared; inspect tailscale serve status")
	}
	b, _ := json.Marshal(ownership{Address: addr, Port: port, Route: expected})
	if err := os.MkdirAll(filepath.Dir(record), 0700); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(record, b, 0600); err != nil {
		return "", nil, err
	}
	cleanup := func() error {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		actual, err := t.current(c, port)
		if err != nil {
			return err
		}
		if string(actual) != string(expected) {
			return errors.New("Tailscale route changed; leaving it untouched")
		}
		if down != nil {
			if err := down(); err != nil {
				return err
			}
		}
		return os.Remove(record)
	}
	return url, cleanup, nil
}
