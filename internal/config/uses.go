package config

import (
	"fmt"
	"strings"

	harness "github.com/shhac/lib-agent-harness"
)

// Use is something the owner chooses an engine for.
type Use string

const (
	// UseAssistant runs the assistant's conversation.
	UseAssistant Use = "assistant"
	// UseRoles runs a team role's turns.
	UseRoles Use = "roles"
	// UseSmall writes suggestions and loading lines.
	UseSmall Use = "small"
	// UseCompact compacts a role's conversation when asked to from outside.
	UseCompact Use = "compact"
	// UseUsage reads a subscription's usage windows.
	UseUsage Use = "usage"
	// UseModels lists the models an engine offers.
	UseModels Use = "models"
	// UseEfforts lists each model's reasoning efforts.
	UseEfforts Use = "efforts"
	// UseBrowser lets QA drive the browser integration the engine itself
	// ships, from its sandboxed session.
	UseBrowser Use = "browser"
	// UseLoopback lets a role's sandboxed shell start the project and reach
	// it on this machine, while every other host stays closed.
	UseLoopback Use = "loopback"
)

// Supports says whether engine can be chosen for use. lib-agent-harness
// says what each engine can do, so an engine it starts to support for
// something is offered for it without a change here.
func Supports(engine string, use Use) bool {
	e := harness.Engine(engine)
	switch use {
	case UseAssistant:
		return harness.Support(e, harness.Complete, harness.Tools).Usable()
	case UseRoles:
		ok, _ := RoleSupport(engine)
		return ok
	case UseSmall:
		return harness.Support(e, harness.Complete, harness.Available).Usable()
	case UseCompact:
		return harness.Support(e, harness.Session, harness.Compact).Usable()
	case UseUsage:
		return harness.Support(e, harness.Account, harness.Quota).Usable()
	case UseModels:
		return harness.Support(e, harness.Models, harness.Available).Usable()
	case UseEfforts:
		return harness.Support(e, harness.Models, harness.Effort).Usable()
	case UseBrowser:
		// Every role runs sandboxed, so the browser is offered only where
		// a sandboxed session admits it.
		return e.Transport() != harness.APITransport && Supports(engine, UseRoles) && harness.Support(e, harness.Session, harness.Browser).Usable() && harness.Support(e, harness.Session, harness.SandboxedBrowser).Usable()
	case UseLoopback:
		return Supports(engine, UseRoles) && harness.Support(e, harness.Session, harness.Loopback).Usable()
	}
	return false
}

// roleSupport is what the harness claims a session on an engine offers; tests
// replace it to stand for a harness that claims more.
var roleSupport = harness.Support

// RoleSupport says whether team roles can run on engine and, when they
// can't, why in the owner's words. A role works in its own sandbox and
// reaches the daemon's tools. API roles also need the workspace read and
// write tools the workbench supplies.
func RoleSupport(engine string) (ok bool, reason string) {
	e := harness.Engine(engine)
	features := []harness.Feature{harness.Sandbox, harness.Tools}
	if e.Transport() == harness.APITransport {
		features = append(features, harness.WorkspaceRead, harness.WorkspaceWrite)
	}
	for _, feature := range features {
		c := roleSupport(e, harness.Session, feature)
		if c.Usable() {
			continue
		}
		if e.Transport() == harness.APITransport {
			return false, fmt.Sprintf("%s can't run team roles on this computer: %s.", EngineLabel(engine), strings.TrimRight(strings.TrimSpace(c.Reason), "."))
		}
		return false, fmt.Sprintf("%s can't run team roles: %s.", EngineLabel(engine), strings.TrimRight(strings.TrimSpace(c.Reason), "."))
	}
	return true, ""
}

// CheckRoleModel requires an explicit model where a team role has no default.
func CheckRoleModel(engine, model string) error {
	if harness.Engine(engine).Transport() == harness.APITransport && strings.TrimSpace(model) == "" {
		return fmt.Errorf("choose a model for this member: another API has no default")
	}
	return nil
}

// CheckRoleEngine refuses an engine team roles can't run on, saying which
// they can and, for a known engine, why not this one.
func CheckRoleEngine(engine string) error {
	ok, reason := RoleSupport(engine)
	if ok {
		return nil
	}
	err := fmt.Errorf("engine must be %s", strings.Join(EnginesFor(UseRoles), " or "))
	if harness.Engine(engine).Transport() == "" {
		return err
	}
	return fmt.Errorf("%w. %s", err, reason)
}

// CheckTemplateRoleEngine keeps engine-only seats on CLIs. API members carry
// the provider and explicit model their sessions need.
func CheckTemplateRoleEngine(engine string) error {
	if harness.Engine(engine).Transport() == harness.APITransport {
		return fmt.Errorf("choose a team member with a provider and model to use another API; template roles need a CLI engine")
	}
	return CheckRoleEngine(engine)
}

// TemplateRoleEngines are the CLI engines usable by an engine-only seat.
func TemplateRoleEngines() []string {
	var engines []string
	for _, engine := range EnginesFor(UseRoles) {
		if harness.Engine(engine).Transport() == harness.CLITransport {
			engines = append(engines, engine)
		}
	}
	return engines
}

// EnginesFor lists the engines that can be chosen for use, in a stable
// order.
func EnginesFor(use Use) []string {
	var names []string
	for _, e := range harness.Engines() {
		if Supports(string(e), use) {
			names = append(names, string(e))
		}
	}
	return names
}

// EngineLabel is how the owner sees an engine named.
func EngineLabel(engine string) string {
	switch harness.Engine(engine) {
	case harness.Codex:
		return "Codex"
	case harness.Claude:
		return "Claude"
	case harness.Grok:
		return "Grok"
	case harness.OpenAICompatible:
		return "Another API"
	}
	return engine
}
