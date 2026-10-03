package cli

import (
	"errors"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

func pauseUntil(duration, until string, now time.Time) (*time.Time, error) {
	if duration != "" && until != "" {
		return nil, errors.New("--for and --until are mutually exclusive")
	}
	if duration != "" {
		d, err := time.ParseDuration(duration)
		if err != nil || d <= 0 {
			return nil, errors.New("--for must be a positive duration")
		}
		end := now.Add(d)
		return &end, nil
	}
	if until == "" {
		return nil, nil
	}
	end, err := time.Parse(time.RFC3339, until)
	if err != nil {
		clock, e := time.Parse("15:04", until)
		if e != nil {
			return nil, errors.New("--until must be RFC 3339 or local HH:MM")
		}
		end = time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
		if !end.After(now) {
			end = end.AddDate(0, 0, 1)
		}
	}
	if !end.After(now) {
		return nil, errors.New("--until must be in the future")
	}
	return &end, nil
}

func registerEngine(root *cobra.Command, o *options) {
	group := &cobra.Command{Use: "engine", Short: "Pause, resume or inspect engines"}
	var duration, until string
	pause := &cobra.Command{Use: "pause <engine>", Short: "Hold new background turns on an engine", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		end, err := pauseUntil(duration, until, time.Now())
		if err != nil {
			return err
		}
		v, err := o.request("PUT", "/api/engines/"+url.PathEscape(args[0])+"/paused", map[string]any{"paused": true, "until": end})
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	pause.Flags().StringVar(&duration, "for", "", "Pause for a duration, such as 1h")
	pause.Flags().StringVar(&until, "until", "", "Pause until RFC 3339 or the next local HH:MM")
	pause.MarkFlagsMutuallyExclusive("for", "until")
	group.AddCommand(pause, &cobra.Command{Use: "resume <engine>", Short: "Resume background turns on an engine", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		v, err := o.request("PUT", "/api/engines/"+url.PathEscape(args[0])+"/paused", map[string]bool{"paused": false})
		if err != nil {
			return err
		}
		return o.emit(v)
	}}, &cobra.Command{Use: "status", Short: "List running, paused and usage-held engines", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		var states []map[string]any
		if err := o.requestInto("GET", "/api/engines", nil, &states); err != nil {
			return err
		}
		for _, state := range states {
			if err := o.emit(state); err != nil {
				return err
			}
		}
		return nil
	}})
	root.AddCommand(group)
}
