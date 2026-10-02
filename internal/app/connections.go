package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
)

func (a *App) DiscoverConnectionProfiles(ctx context.Context, tool string) (connections.Discovery, error) {
	if a.Demo {
		return connections.Discovery{Tool: tool, Profiles: []connections.Profile{}, Detail: "Demo mode does not inspect local accounts"}, nil
	}
	if err := a.refuseWhileStopping(); err != nil {
		return connections.Discovery{}, err
	}
	return a.connectionClient.Discover(ctx, tool)
}
func (a *App) runConnectionTool(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if name == "list_connections" {
		if err := args(raw, &struct{}{}); err != nil {
			return nil, err
		}
		type entry struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Tool       string   `json:"tool"`
			Profiles   []string `json:"profiles"`
			Operations []string `json:"operations"`
			Detail     string   `json:"detail"`
		}
		result := []entry{}
		for _, c := range a.Config().Connections {
			detail := "Read-only; select an explicit configured profile on every query"
			if c.Tool == "lin" {
				detail = "Linear changes are off; lin only reads"
				if c.AllowWrites {
					detail = "Linear changes are allowed through lin"
				}
			}
			if c.Tool == "agent-notion" {
				detail = "Uses the CLI default account; pass an empty profile on every query"
			}
			result = append(result, entry{c.ID, c.Name, c.Tool, c.Profiles, connections.Operations(c.Tool), detail})
		}
		return result, nil
	}
	if a.Demo {
		return nil, errors.New("demo mode does not query external accounts")
	}
	var q connections.Query
	if err := args(raw, &q); err != nil {
		return nil, err
	}
	return a.connectionClient.Query(ctx, a.Config().Connections, q)
}

// syncCLIConnections imports bounded assignment metadata only for connections
// whose owner explicitly enabled project imports. Account access alone is not
// enrollment. No source writes or worker starts occur here.
func (a *App) syncCLIConnections(ctx context.Context) error {
	cfg := a.Config()
	var failures []error
	for _, binding := range cfg.Connections {
		if binding.Tool != "lin" || !binding.ImportAssignments {
			continue
		}
		count, errs := a.importAssignments(ctx, cfg.Connections, binding)
		failures = append(failures, errs...)
		if len(errs) > 0 {
			a.Status("connection:"+binding.ID, binding.Name, "error", errs[len(errs)-1].Error())
			continue
		}
		a.Status("connection:"+binding.ID, binding.Name, "connected", fmt.Sprintf("%d assigned issues", count))
	}
	return errors.Join(failures...)
}

// importAssignments keeps going past a failing profile so one broken account
// doesn't hide the others' assignments.
func (a *App) importAssignments(ctx context.Context, bindings []config.Connection, binding config.Connection) (int, []error) {
	count := 0
	var failures []error
	for _, profile := range binding.Profiles {
		imported, err := a.importProfileAssignments(ctx, bindings, binding, profile)
		count += imported
		if err != nil {
			failures = append(failures, err)
		}
	}
	return count, failures
}

func (a *App) importProfileAssignments(ctx context.Context, bindings []config.Connection, binding config.Connection, profile string) (int, error) {
	result, err := a.connectionClient.Query(ctx, bindings, connections.Query{ConnectionID: binding.ID, Profile: profile, Operation: "assignments"})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, raw := range result.Data {
		var issue assignedIssue
		if json.Unmarshal(raw, &issue) != nil || !issue.importable() {
			continue
		}
		_, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: issue.Identifier + " · " + issue.Title, SourceDescription: fmt.Sprintf("Linear assignment from %s / %s. Status: %s. Read the full issue using this connection before writing its brief.", binding.Name, profile, issue.Status), SourceID: "lin:" + binding.ID + ":" + profile + ":" + issue.ID})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

type assignedIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	StatusType string `json:"statusType"`
}

// importable leaves out finished work and records too thin to name a project.
func (i assignedIssue) importable() bool {
	return i.ID != "" && i.Title != "" && i.StatusType != "completed" && i.StatusType != "canceled"
}
