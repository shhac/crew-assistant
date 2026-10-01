package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/text"
)

// RunRecipe is how QA starts a code project and reaches it on this machine,
// so it can use the app as well as run the check. Setup runs first, offline:
// dependencies come in through Prepare. Start runs in the background with
// PORT set, and URL, with {port} in it, is where the app answers. Ready is
// a command that succeeds once the app is ready, or empty to wait until URL
// answers.
type RunRecipe struct {
	Setup string `json:"setup,omitempty"`
	Start string `json:"start"`
	URL   string `json:"url"`
	Ready string `json:"ready,omitempty"`
}

// PortPlaceholder is what a recipe's URL writes for the port QA is given.
const PortPlaceholder = "{port}"

const (
	maxRecipeCommand = 500
	maxRecipeURL     = 300
)

// Address is where the app answers on port.
func (r RunRecipe) Address(port int) string {
	return strings.ReplaceAll(r.URL, PortPlaceholder, strconv.Itoa(port))
}

// Trimmed is the recipe with the space around each field taken off.
func (r RunRecipe) Trimmed() RunRecipe {
	return RunRecipe{Setup: strings.TrimSpace(r.Setup), Start: strings.TrimSpace(r.Start), URL: strings.TrimSpace(r.URL), Ready: strings.TrimSpace(r.Ready)}
}

// validate checks a recipe on its own: a start command, and a URL on this
// machine only, over http, with the port QA is given.
func (r RunRecipe) validate() error {
	if strings.TrimSpace(r.Start) == "" || strings.TrimSpace(r.URL) == "" {
		return errors.New("a run recipe needs the command that starts the app and the URL it answers on")
	}
	for name, command := range map[string]string{"setup": r.Setup, "start": r.Start, "ready": r.Ready} {
		if len(command) > maxRecipeCommand {
			return fmt.Errorf("the %s command must fit in %d characters", name, maxRecipeCommand)
		}
		if strings.ContainsFunc(command, unicode.IsControl) {
			return fmt.Errorf("the %s command must be one line", name)
		}
	}
	if len(r.URL) > maxRecipeURL {
		return fmt.Errorf("the URL must fit in %d characters", maxRecipeURL)
	}
	// {port} must be the address's port and appear nowhere else: QA reaches
	// the app on the port its check was given, so apps of checks running
	// side by side never meet on one.
	const probe = 54321
	u, err := url.Parse(r.Address(probe))
	if strings.Count(r.URL, PortPlaceholder) != 1 || err != nil || u.Port() != strconv.Itoa(probe) {
		return errors.New("the URL's port must be {port}, and {port} must appear nowhere else, such as http://127.0.0.1:{port}/, since each check is given a port of its own")
	}
	if u.Scheme != "http" || u.User != nil {
		return errors.New("the URL must be a plain http address, such as http://127.0.0.1:{port}/")
	}
	if host := u.Hostname(); host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("the URL must be on this machine: 127.0.0.1, localhost or [::1]")
	}
	return nil
}

// Describe is the recipe as the owner reads it.
func (r RunRecipe) Describe() string {
	var b strings.Builder
	if r.Setup != "" {
		fmt.Fprintf(&b, "Setup: %s\n", r.Setup)
	}
	fmt.Fprintf(&b, "Start: %s\nURL: %s\n", r.Start, r.URL)
	if r.Ready != "" {
		fmt.Fprintf(&b, "Ready when: %s\n", r.Ready)
	} else {
		b.WriteString("Ready when: the URL answers\n")
	}
	return b.String()
}

// Browser is a member's browser setting, or a QA seat's for using the app.
type Browser = config.Browser

// validateBrowser checks a browser setting on engine. forRole is whether the
// setting belongs where it is: any member may allow the browser, while a
// seat's own setting is QA's, for using the app.
func validateBrowser(b Browser, engine string, forRole bool) error {
	if b.On && !forRole {
		return errors.New("a seat's browser setting is QA's; give the QA role or switch the browser off")
	}
	return b.Validate(engine)
}

// DecisionRunRecipe is a run recipe the researcher or the PM proposed for
// the owner to accept; nothing applies before they do.
const DecisionRunRecipe = "run-recipe"

// Choices on a proposed run recipe.
const (
	ChoiceUseRecipe = "Use this recipe"
	ChoiceNotNow    = "Not now"
)

// ProposeRunRecipe brings the owner a run recipe proposed for a code
// project. One proposal waits at a time.
func (s *Service) ProposeRunRecipe(ctx context.Context, projectID, by string, recipe RunRecipe, why string) (Decision, error) {
	recipe = recipe.Trimmed()
	if err := recipe.validate(); err != nil {
		return Decision{}, err
	}
	why = text.Clip(strings.TrimSpace(why), 600)
	var out Decision
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		if p.Playbook == nil || p.Playbook.Medium != MediumGit {
			return errors.New("a run recipe is for code teams")
		}
		for _, d := range v.Decisions {
			if d.ProjectID == projectID && d.Kind == DecisionRunRecipe && d.Status == DecisionOpen {
				return fmt.Errorf("a run recipe is already waiting for the owner: %w", ErrConflict)
			}
		}
		context := recipe.Describe()
		if why != "" {
			context += "\nWhy: " + why + "\n"
		}
		if p.Playbook.Run != nil {
			context += "\nIt replaces the recipe in use now:\n" + p.Playbook.Run.Describe()
		}
		context += "\nWith it, QA starts the app on this machine only and uses it, as well as running the check."
		now := s.now().UTC()
		out = Decision{ID: uid(), Kind: DecisionRunRecipe, ProjectID: projectID, Title: fmt.Sprintf("%s proposes how QA runs %s", by, p.Title), Context: context, Recommendation: "Use it if these commands start the app", Choices: []string{ChoiceUseRecipe, ChoiceNotNow}, Status: DecisionOpen, CreatedAt: now, Run: &recipe}
		v.Decisions = append(v.Decisions, out)
		record(v, now, projectID, "decision.opened", out.Title)
		return nil
	})
	return out, err
}

// acceptRunRecipe sets the recipe the owner accepted, checked as the
// project's playbook is, in the same change as the owner's choice. Tasks
// already under way keep the playbook they started with.
func acceptRunRecipe(v *Snapshot, d *Decision) error {
	p := project(v, d.ProjectID)
	if p == nil || p.Playbook == nil || d.Run == nil {
		return fmt.Errorf("the project no longer has a team this recipe is for: %w", ErrConflict)
	}
	playbook := *p.Playbook
	recipe := *d.Run
	playbook.Run = &recipe
	if err := playbook.Validate(); err != nil {
		return fmt.Errorf("this recipe can't be used: %w", err)
	}
	p.Playbook = &playbook
	return nil
}
