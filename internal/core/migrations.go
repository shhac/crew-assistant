package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// migrations upgrade a state document from the schema of its key to the
// next. Each works on the document as JSON, since the types it was written
// with are gone, and changes only what its version changed.
var migrations = map[int]func(doc map[string]any) error{
	2: pullRequestsToggle,
	3: repositoriesAndTeams,
}

// repositoryKeys and teamKeys are the parts of a project's playbook that
// schema 4 keeps with its repository and its team.
var (
	repositoryKeys = map[string]string{"repo": "path", "check": "check", "prepare": "prepare", "check_in_copy": "check_in_copy", "check_loopback": "check_loopback", "run": "run"}
	teamKeys       = map[string]string{"roles": "roles", "max_rounds": "max_rounds", "template": "template"}
)

// repositoriesAndTeams splits each project's playbook between its owners.
// A code project becomes one repository, holding its code settings, and
// one team, holding its seats, PM and rounds, both named for the project
// and used by it alone; the project keeps the rest as its settings. A code
// project not yet given a repository gets only the team. Any other project
// keeps its whole playbook as its own settings, as a project with no team
// or repository does. Tasks keep the playbooks they pinned, and every
// project's playbook reads back exactly as it was.
func repositoriesAndTeams(doc map[string]any) error {
	snapshot, _ := doc["snapshot"].(map[string]any)
	if snapshot == nil {
		return nil
	}
	repositories, _ := snapshot["repositories"].([]any)
	teams, _ := snapshot["teams"].([]any)
	projects, _ := snapshot["projects"].([]any)
	for _, item := range projects {
		p, _ := item.(map[string]any)
		if p == nil {
			continue
		}
		playbook, ok := p["playbook"].(map[string]any)
		delete(p, "playbook")
		if !ok {
			continue
		}
		p["settings"] = playbook
		if playbook["medium"] != MediumGit {
			continue
		}
		id, _ := p["id"].(string)
		title, _ := p["title"].(string)
		if repo, _ := playbook["repo"].(string); repo != "" {
			r := map[string]any{"id": "repo-" + id, "name": filepath.Base(repo)}
			moveKeys(playbook, r, repositoryKeys)
			repositories = append(repositories, r)
			p["scope"] = map[string]any{"repositories": []any{map[string]any{"id": r["id"]}}}
		}
		t := map[string]any{"id": "team-" + id, "name": title}
		moveKeys(playbook, t, teamKeys)
		// The project's settings keep the template too: it says what kind of
		// work the project is.
		if template, ok := t["template"]; ok {
			playbook["template"] = template
		}
		teams = append(teams, t)
		p["team"] = t["id"]
	}
	snapshot["repositories"], snapshot["teams"] = repositories, teams
	return nil
}

// moveKeys moves each of keys present in from to its name in to.
func moveKeys(from, to map[string]any, keys map[string]string) {
	for key, name := range keys {
		if value, ok := from[key]; ok {
			to[name] = value
			delete(from, key)
		}
	}
}

// pullRequestsToggle turns each landing policy that landed by pull request,
// the project's and every task's own copy, into one with pull requests on.
// Without them it would land on a new branch, which moves nothing. Approval
// was the owner's, before the pull request opened, and it merged once ready;
// so it still opens on the owner's approval, or without one where none was
// asked, and merges once ready.
func pullRequestsToggle(doc map[string]any) error {
	snapshot, _ := doc["snapshot"].(map[string]any)
	for _, list := range []string{"projects", "tasks"} {
		items, _ := snapshot[list].([]any)
		for _, item := range items {
			entity, _ := item.(map[string]any)
			playbook, _ := entity["playbook"].(map[string]any)
			land, _ := playbook["land"].(map[string]any)
			if land == nil || land["via"] != "pull-request" {
				continue
			}
			delete(land, "via")
			land["pull_requests"] = true
			if method, ok := land["method"]; ok {
				land["merge"] = method
				delete(land, "method")
			}
			land["open"] = OpenOwner
			if land["approve"] == ApproveNone {
				land["open"] = OpenImplementer
			}
			land["approve"] = ApproveNone
		}
	}
	return nil
}

// upgrade brings state written by an earlier schema up to this build's,
// once, keeping a copy of the database as it was beside it; state from a
// later schema is refused before anything reads it.
func (s *Store) upgrade(ctx context.Context, path string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	data, err := payload(ctx, conn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if err = json.Unmarshal([]byte(data), &head); err != nil {
		return fmt.Errorf("decode durable state: %w", err)
	}
	switch {
	case head.Schema == stateSchema:
		return nil
	case head.Schema > stateSchema:
		return fmt.Errorf("%w: it is schema %d, from a newer build, and this one reads %d; upgrade crew-assistant", ErrStateSchema, head.Schema, stateSchema)
	}
	upgraded, err := upgradeState([]byte(data), head.Schema, stateSchema, migrations)
	if err != nil {
		return err
	}
	// SQLite copies a database only outside a transaction, so the upgrade
	// below makes sure nothing wrote in between.
	if !s.temporaryState {
		if err = backUp(ctx, conn, path, head.Schema); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if current, err := payload(ctx, conn); err != nil || current != data {
		return errors.Join(errors.New("state changed while it was being upgraded; start again"), err)
	}
	if _, err = conn.ExecContext(ctx, "UPDATE state SET payload=?, version=version+1 WHERE id=1", string(upgraded)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func payload(ctx context.Context, conn *sql.Conn) (string, error) {
	var data string
	err := conn.QueryRowContext(ctx, "SELECT payload FROM state WHERE id=1").Scan(&data)
	return data, err
}

// upgradeState runs the migrations from schema from up to schema to.
func upgradeState(data []byte, from, to int, migrations map[int]func(map[string]any) error) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	// Numbers stay exactly as written, never rounded through float64.
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode durable state: %w", err)
	}
	for v := from; v < to; v++ {
		migrate, ok := migrations[v]
		if !ok {
			return nil, fmt.Errorf("%w: no upgrade from schema %d to %d", ErrStateSchema, v, v+1)
		}
		if err := migrate(doc); err != nil {
			return nil, fmt.Errorf("upgrade state from schema %d: %w", v, err)
		}
	}
	doc["schema"] = to
	return json.Marshal(doc)
}

// backUp copies the database, as the schema it was written in, beside it.
func backUp(ctx context.Context, conn *sql.Conn, path string, schema int) error {
	dir, base := filepath.Split(path)
	backup := filepath.Join(dir, fmt.Sprintf("%s.schema-%d.%s.bak", base, schema, time.Now().UTC().Format("20060102T150405Z")))
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		return fmt.Errorf("back up state before upgrading it: %w", err)
	}
	return os.Chmod(backup, 0o600)
}
