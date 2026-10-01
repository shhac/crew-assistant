package connections

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

type LinearPage struct {
	Nodes    []core.LinearIssue `json:"nodes"`
	PageInfo *struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

const LinearPickUpQuery = `query CrewPickUp($filter: IssueFilter!, $after: String) { issues(first: 10, after: $after, filter: $filter, orderBy: createdAt) { nodes { id identifier title url } pageInfo { hasNextPage endCursor } } }`

const LinearProjectTeamsQuery = `query CrewProjectTeams($id: String!) { project(id: $id) { teams(first: 50) { nodes { id name } pageInfo { hasNextPage } } } }`

const LinearIssueContextQuery = `query CrewIssueContext($id: String!) { issue(id: $id) { id description } }`
const LinearDescriptionLimit = 8 * 1024
const LinearDescriptionOmitted = "[Description omitted: the issue exceeded the 128KiB read limit. See the source issue for the full description.]"
const LinearDescriptionTruncated = "\n[Description truncated. See the source issue for the full description.]"

// LinearIssueContext isolates description reads from metadata pages. Oversized
// content is explicitly omitted; authentication, transport and API errors remain
// failures, so a failed read never becomes an empty description.
func (s *LinearSession) LinearIssueContext(ctx context.Context, l core.LinearLink, issue core.LinearIssue) (string, error) {
	if s == nil || s.id != l.ConnectionID || s.profile != l.Profile {
		return "", errors.New("Linear session account mismatch")
	}
	if issue.ID == "" || len(issue.ID) > 256 || strings.ContainsAny(issue.ID, "\x00\r\n") {
		return "", errors.New("invalid Linear issue ID")
	}
	vars, _ := json.Marshal(map[string]string{"id": issue.ID})
	data, err := s.read(ctx, []string{"api", "query", LinearIssueContextQuery, "--variables", string(vars)})
	if errors.Is(err, ErrResponseTooLarge) {
		return LinearDescriptionOmitted, nil
	}
	if err != nil {
		return "", err
	}
	var out struct {
		Issue  *core.LinearIssue `json:"issue"`
		Errors json.RawMessage   `json:"errors"`
		Error  json.RawMessage   `json:"error"`
	}
	if json.Unmarshal(data, &out) != nil || out.Issue == nil || out.Issue.ID != issue.ID || len(out.Errors) > 0 || len(out.Error) > 0 {
		return "", errors.New("Linear returned invalid issue context")
	}
	description := out.Issue.Description
	if len(description) > LinearDescriptionLimit {
		description = text.Clip(description, LinearDescriptionLimit-len(LinearDescriptionTruncated)-3) + LinearDescriptionTruncated
	}
	return description, nil
}

// LinearSession holds one discovered account for a bounded group of reads.
// Its private fields keep discovery explicit and prevent account substitution.
type LinearSession struct {
	client      Client
	id, profile string
}

// LinearAccount discovers once for a bounded group of reads, never globally.
func (c Client) LinearAccount(ctx context.Context, bindings []config.Connection, id, profile string) (*LinearSession, error) {
	if err := core.LinearBinding(bindings, id, profile); err != nil {
		return nil, err
	}
	d, err := c.Discover(ctx, "lin")
	if err != nil {
		return nil, err
	}
	known := false
	for _, p := range d.Profiles {
		if p.Name == profile {
			known = true
		}
	}
	if !d.Available || !known {
		return nil, errors.New("selected Linear account is unavailable; check lin authentication")
	}
	return &LinearSession{client: c, id: id, profile: profile}, nil
}
func (c Client) linearRead(ctx context.Context, bindings []config.Connection, id, profile string, args []string) ([]byte, error) {
	session, err := c.LinearAccount(ctx, bindings, id, profile)
	if err != nil {
		return nil, err
	}
	return session.read(ctx, args)
}
func (s *LinearSession) read(ctx context.Context, args []string) ([]byte, error) {
	if s == nil || s.client.Run == nil {
		return nil, errors.New("Linear session is unavailable")
	}

	data, err := s.client.Run(ctx, "lin", append(args, "--format", "json", "--workspace", s.profile))
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			return nil, ErrResponseTooLarge
		}
		return nil, errors.New("Linear read failed; check the selected CLI account and permissions")
	}
	if len(data) > 128*1024 {
		return nil, ErrResponseTooLarge
	}
	return data, nil
}
func (c Client) LinearOptions(ctx context.Context, bindings []config.Connection, id, profile, kind, team string) ([]json.RawMessage, error) {
	var args []string
	switch kind {
	case "project-teams":
		if !core.ValidLinearID(team) {
			return nil, errors.New("choose a Linear project")
		}
		vars, _ := json.Marshal(map[string]string{"id": team})
		data, err := c.linearRead(ctx, bindings, id, profile, []string{"api", "query", LinearProjectTeamsQuery, "--variables", string(vars)})
		if err != nil {
			return nil, err
		}
		var out struct {
			Project *struct {
				Teams *struct {
					Nodes    []json.RawMessage `json:"nodes"`
					PageInfo *struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"teams"`
			} `json:"project"`
			Errors json.RawMessage `json:"errors"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(data, &out) != nil || len(out.Errors) > 0 || len(out.Error) > 0 || out.Project == nil || out.Project.Teams == nil || out.Project.Teams.PageInfo == nil || out.Project.Teams.PageInfo.HasNextPage || len(out.Project.Teams.Nodes) > 50 {
			return nil, errors.New("Linear returned incomplete project teams")
		}
		if out.Project.Teams.Nodes == nil {
			return []json.RawMessage{}, nil
		}
		return out.Project.Teams.Nodes, nil
	case "teams":
		args = []string{"team", "list", "--limit", "100"}
	case "projects":
		args = []string{"project", "list", "--limit", "100"}
		if team != "" {
			if !core.ValidLinearID(team) {
				return nil, errors.New("invalid team")
			}
			args = append(args, "--team", team)
		}
	case "states":
		if !core.ValidLinearID(team) {
			return nil, errors.New("choose a team")
		}
		args = []string{"team", "states", team}
	case "users":
		args = []string{"user", "list", "--limit", "100"}
	default:
		return nil, errors.New("unknown Linear options")
	}
	session, err := c.LinearAccount(ctx, bindings, id, profile)
	if err != nil {
		return nil, err
	}
	rows := []json.RawMessage{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 5; page++ {
		argv := append([]string{}, args...)
		if cursor != "" {
			argv = append(argv, "--cursor", cursor)
		}
		data, err := session.read(ctx, argv)
		if err != nil {
			return nil, err
		}
		batch, next, err := linearOptionPage(data)
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
		if len(rows) > 500 {
			return nil, errors.New("Linear option list exceeds 500 records; it was not loaded completely")
		}
		if next == "" {
			return rows, nil
		}
		if kind == "states" || seen[next] {
			return nil, errors.New("Linear option pagination did not advance")
		}
		seen[next] = true
		cursor = next
	}
	return nil, errors.New("Linear option list has more than five pages; it was not loaded completely")
}

// lin uses the family's JSON list envelope with @pagination. Bare arrays from
// non-paginated reads remain valid, but a full page without metadata is refused.
func linearOptionPage(data []byte) ([]json.RawMessage, string, error) {
	var rows []json.RawMessage
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &rows) != nil {
		if json.Unmarshal(data, &envelope) != nil || envelope["data"] == nil || json.Unmarshal(envelope["data"], &rows) != nil || rows == nil {
			return nil, "", errors.New("Linear returned invalid option data")
		}
	}
	if rows == nil {
		return nil, "", errors.New("Linear returned invalid option data")
	}
	if len(rows) > 100 {
		return nil, "", errors.New("Linear option page exceeds 100 records")
	}
	var pagination *struct {
		HasMore    *bool  `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if envelope != nil {
		if len(envelope["errors"]) > 0 || len(envelope["error"]) > 0 {
			return nil, "", errors.New("Linear option read failed")
		}
		if raw, ok := envelope["@pagination"]; ok {
			if json.Unmarshal(raw, &pagination) != nil || pagination == nil || pagination.HasMore == nil {
				return nil, "", errors.New("Linear returned invalid option pagination")
			}
		} else {
			for key := range envelope {
				if strings.Contains(strings.ToLower(key), "pagina") || strings.Contains(strings.ToLower(key), "cursor") || key == "pageInfo" {
					return nil, "", errors.New("Linear returned unsupported option pagination")
				}
			}
		}
	}
	if pagination == nil {
		if len(rows) == 100 {
			return nil, "", errors.New("Linear returned a full option page without pagination; it was not loaded completely")
		}
		return rows, "", nil
	}
	if !*pagination.HasMore {
		return rows, "", nil
	}
	next := pagination.NextCursor
	if next == "" || len(next) > 256 || strings.ContainsAny(next, "\x00\r\n") {
		return nil, "", errors.New("Linear returned invalid option cursor")
	}
	return rows, next, nil
}

func (s *LinearSession) PickUpIssues(ctx context.Context, l core.LinearLink, after string) (LinearPage, error) {
	if s == nil || s.id != l.ConnectionID || s.profile != l.Profile {
		return LinearPage{}, errors.New("Linear session account mismatch")
	}
	var page LinearPage
	if err := core.ValidateLinear(l); err != nil {
		return page, err
	}
	if after != "" && (len(after) > 256 || strings.ContainsAny(after, "\x00\r\n")) {
		return page, errors.New("invalid cursor")
	}
	filter := map[string]any{l.Kind: map[string]any{"id": map[string]any{"eq": l.ID}}}
	if len(l.Rules.States) > 0 {
		filter["state"] = map[string]any{"name": map[string]any{"in": l.Rules.States}}
	}
	switch l.Rules.Assignee {
	case "unassigned":
		filter["assignee"] = map[string]any{"null": true}
	case "me":
		filter["assignee"] = map[string]any{"isMe": map[string]any{"eq": true}}
	case "users":
		ids := []string{}
		for _, u := range l.Rules.Users {
			ids = append(ids, u.ID)
		}
		filter["assignee"] = map[string]any{"id": map[string]any{"in": ids}}
	}
	var cursor any
	if after != "" {
		cursor = after
	}
	vars, _ := json.Marshal(map[string]any{"filter": filter, "after": cursor})
	data, err := s.read(ctx, []string{"api", "query", LinearPickUpQuery, "--variables", string(vars)})
	if err != nil {
		return page, err
	}
	var out struct {
		Issues LinearPage      `json:"issues"`
		Errors json.RawMessage `json:"errors"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &out) != nil || len(out.Errors) > 0 || len(out.Error) > 0 || out.Issues.PageInfo == nil || len(out.Issues.Nodes) > 10 {
		return page, errors.New("Linear returned an invalid issue page")
	}
	if info := out.Issues.PageInfo; info.HasNextPage && (info.EndCursor == "" || len(info.EndCursor) > 256 || strings.ContainsAny(info.EndCursor, "\x00\r\n")) {
		return page, errors.New("Linear returned an invalid page cursor")
	}
	return out.Issues, nil
}
