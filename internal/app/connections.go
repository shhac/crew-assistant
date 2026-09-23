package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
)

func (a *App) DiscoverConnectionProfiles(ctx context.Context, tool string) (connections.Discovery, error) {
	if a.Demo {
		return connections.Discovery{Tool: tool, Profiles: []connections.Profile{}, Detail: "Demo mode does not inspect local accounts"}, nil
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
		count := 0
		var connectionErr error
		for _, profile := range binding.Profiles {
			result, err := a.connectionClient.Query(ctx, cfg.Connections, connections.Query{ConnectionID: binding.ID, Profile: profile, Operation: "assignments"})
			if err != nil {
				connectionErr = err
				failures = append(failures, err)
				continue
			}
			for _, raw := range result.Data {
				var issue struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
					Title      string `json:"title"`
					Status     string `json:"status"`
					StatusType string `json:"statusType"`
				}
				if err = json.Unmarshal(raw, &issue); err != nil || issue.ID == "" || issue.Title == "" || issue.StatusType == "completed" || issue.StatusType == "canceled" {
					continue
				}
				_, err = a.Core.CreateProject(ctx, core.ProjectInput{Title: issue.Identifier + " · " + issue.Title, Description: fmt.Sprintf("Linear assignment from %s / %s. Status: %s. Read the full issue using this connection before commissioning work.", binding.Name, profile, issue.Status), SourceID: "lin:" + binding.ID + ":" + profile + ":" + issue.ID, AcceptanceCriteria: "Deliver the source outcome: " + issue.Title + ". Read the full source issue and define measurable acceptance checks before commissioning work."})
				if err != nil {
					connectionErr = err
					failures = append(failures, err)
					break
				}
				count++
			}
		}
		if connectionErr != nil {
			a.Status("connection:"+binding.ID, binding.Name, "error", connectionErr.Error())
		} else {
			a.Status("connection:"+binding.ID, binding.Name, "connected", fmt.Sprintf("%d active assignments from selected CLI accounts; bounded to 50 per account. No work starts without a commission.", count))
		}
	}
	return errors.Join(failures...)
}
