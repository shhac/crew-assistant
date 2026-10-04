package cli

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/spf13/cobra"
)

func (o *options) setAutopilotMode(id string, mode autopilot.Mode, revision uint64) error {
	if err := canonicalUpgradeOptions(o); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.statePath), 0700); err != nil {
		return err
	}
	lock := flock.New(o.statePath + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		_, err = o.request("PUT", "/api/autopilot/modes/"+url.PathEscape(id), map[string]any{"mode": mode, "revision": revision})
		return err
	}
	defer lock.Unlock()
	_, err = config.SetAutopilotMode(o.configPath, id, mode, revision)
	return err
}

func autopilotCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{Use: "autopilot", Short: "Inspect saved modes and checked assistant actions"}
	cmd.AddCommand(&cobra.Command{Use: "summary", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		v, err := o.request("GET", "/api/autopilot/summary", nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	cmd.AddCommand(&cobra.Command{Use: "catalog", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		// Offline catalog explicitly reports no runtime implementations.
		if _, err := o.runtime(); err == nil {
			v, err := o.request("GET", "/api/autopilot", nil)
			if err != nil {
				return err
			}
			return o.emit(v)
		}
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		return o.emit(map[string]any{"functions": autopilot.Catalog(), "settings": cfg.Autopilot})
	}})
	cmd.AddCommand(&cobra.Command{Use: "get <function>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		mode, err := cfg.Autopilot.EffectiveMode(args[0])
		if err != nil {
			return err
		}
		return o.emit(map[string]any{"id": args[0], "mode": mode, "revision": cfg.Autopilot.Revisions[args[0]]})
	}})
	for _, verb := range []string{"set", "unset"} {
		use, n := verb+" <function>", 1
		if verb == "set" {
			use += " <off|suggest|act>"
			n = 2
		}
		cmd.AddCommand(&cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(o.configPath)
			if err != nil {
				return err
			}
			mode := autopilot.Mode("")
			if verb == "set" {
				mode = autopilot.Mode(args[1])
			}
			if err := o.setAutopilotMode(args[0], mode, cfg.Autopilot.Revisions[args[0]]); err != nil {
				return err
			}
			return o.emit(map[string]bool{"saved": true})
		}})
	}
	cmd.AddCommand(&cobra.Command{Use: "permission <project> <true|false> <revision>", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		allowed, err := strconv.ParseBool(args[1])
		if err != nil {
			return err
		}
		revision, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return err
		}
		v, err := o.request("PUT", "/api/projects/"+url.PathEscape(args[0])+"/operator-permission", map[string]any{"allowed": allowed, "revision": revision})
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	cmd.AddCommand(&cobra.Command{Use: "action <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		v, err := o.request("GET", "/api/autopilot/actions/"+url.PathEscape(args[0]), nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	for _, verb := range []string{"approve", "cancel", "undo", "override", "reconcile"} {
		use, n := verb+" <id> <revision>", 2
		if verb == "override" {
			use += " <replacement-json>"
			n = 3
		}
		cmd.AddCommand(&cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
			revision, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				return err
			}
			body := map[string]any{"revision": revision}
			if verb == "override" {
				var replacement core.ConcreteAction
				if err := json.Unmarshal([]byte(args[2]), &replacement); err != nil {
					return err
				}
				body["replacement"] = replacement
			}
			v, err := o.request("POST", "/api/autopilot/actions/"+url.PathEscape(args[0])+"/"+verb, body)
			if err != nil {
				return err
			}
			return o.emit(v)
		}})
	}
	pending := &cobra.Command{Use: "pending", Args: cobra.NoArgs}
	var pendingAfter string
	var pendingLimit int
	pending.Flags().StringVar(&pendingAfter, "after", "", "Last action ID")
	pending.Flags().IntVar(&pendingLimit, "limit", 50, "Page size, at most 200")
	pending.RunE = func(cmd *cobra.Command, args []string) error {
		v, err := o.request("GET", "/api/autopilot/pending?"+url.Values{"after": {pendingAfter}, "limit": {strconv.Itoa(pendingLimit)}}.Encode(), nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}
	cmd.AddCommand(&cobra.Command{Use: "digest <on|off|at> [HH:MM]", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		d := cfg.Autopilot.DailyDigest
		switch args[0] {
		case "on", "off":
			if len(args) != 1 {
				return errors.New("on/off takes no time")
			}
			d.Enabled = args[0] == "on"
		case "at":
			if len(args) != 2 {
				return errors.New("at requires HH:MM")
			}
			d.At = args[1]
		default:
			return errors.New("expected on, off or at")
		}
		if err := d.Validate(); err != nil {
			return err
		}
		if err := o.setAutopilotDigest(d); err != nil {
			return err
		}
		return o.emit(map[string]bool{"saved": true})
	}})
	cmd.AddCommand(pending)
	var project, task, cursor string
	var limit int
	var after int64
	var forward bool
	history := &cobra.Command{Use: "history", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if limit < 1 || limit > 200 || after < 0 {
			return errors.New("invalid history bounds")
		}
		q := url.Values{"project_id": {project}, "task_id": {task}, "cursor": {cursor}, "limit": {strconv.Itoa(limit)}, "after": {strconv.FormatInt(after, 10)}, "forward": {strconv.FormatBool(forward)}}
		v, err := o.request("GET", "/api/autopilot/history?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	history.Flags().StringVar(&project, "project", "", "Project filter")
	history.Flags().StringVar(&task, "task", "", "Task filter (requires project)")
	history.Flags().StringVar(&cursor, "cursor", "", "Next page cursor")
	history.Flags().IntVar(&limit, "limit", 50, "Page size, at most 200")
	history.Flags().Int64Var(&after, "after", 0, "Audit sequence for forward reads")
	history.Flags().BoolVar(&forward, "forward", false, "Read oldest first for summaries")
	cmd.AddCommand(history)
	return cmd
}

func (o *options) setAutopilotDigest(d autopilot.DailyDigest) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if err := canonicalUpgradeOptions(o); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.statePath), 0700); err != nil {
		return err
	}
	lock := flock.New(o.statePath + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		_, err = o.request("PUT", "/api/autopilot/digest", d)
		return err
	}
	defer lock.Unlock()
	_, err = config.SetAutopilotDigest(o.configPath, d, d.Revision)
	return err
}
