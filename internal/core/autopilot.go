package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
)

func strictJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

type OperatorPermission struct {
	Allowed  bool      `json:"allowed"`
	Revision uint64    `json:"revision"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at,omitempty"`
}

// SetOperatorPermission is an owner action, deliberately absent from assistant
// and PM tools. It changes neither release policy nor the global function mode.
func (s *Service) SetOperatorPermission(ctx context.Context, id string, allowed bool, expected uint64) (Project, error) {
	return s.editProject(ctx, id, func(p *Project, v *Snapshot) error {
		if p.OperatorPermission.Revision != expected {
			return ErrConflict
		}
		p.OperatorPermission = OperatorPermission{allowed, expected + 1, "owner", s.now().UTC()}
		record(v, s.now().UTC(), id, "autopilot.permission", fmt.Sprintf("Owner set operator permission: %t", allowed))
		return nil
	})
}

// ConcreteAction is immutable once proposed. Rule and executor identities come
// from a registered capability, never model-supplied arguments.
type ConcreteAction struct {
	TargetDigest       string          `json:"target_digest,omitempty"`
	Kind               string          `json:"kind"`
	ProjectID          string          `json:"project_id"`
	TaskID             string          `json:"task_id,omitempty"`
	Commit             string          `json:"commit,omitempty"`
	TargetVersion      uint64          `json:"target_version"`
	PermissionRevision uint64          `json:"permission_revision"`
	Args               json.RawMessage `json:"args"`
}

type AutopilotActorKind string

const (
	AutopilotOwner     AutopilotActorKind = "owner"
	AutopilotAssistant AutopilotActorKind = "assistant"
)

type AutopilotAction struct {
	ExecutorKind  AutopilotActorKind `json:"executor_kind,omitempty"`
	ID            string             `json:"id"`
	Source        string             `json:"source"`
	Function      string             `json:"function"`
	RuleVersion   string             `json:"rule_version"`
	Revision      uint64             `json:"revision"`
	Action        ConcreteAction     `json:"action"`
	Reason        string             `json:"reason"`
	AssistantID   string             `json:"assistant_id"`
	AssistantName string             `json:"assistant_name"`
	ProposedAt    time.Time          `json:"proposed_at"`
	Status        string             `json:"status"`
	Detail        string             `json:"detail,omitempty"`
	Approver      string             `json:"approver,omitempty"`
	Executor      string             `json:"executor,omitempty"`
	Undo          json.RawMessage    `json:"undo,omitempty"`
	UndoVersion   uint64             `json:"undo_version,omitempty"`
}

type AutopilotAudit struct {
	ActorKind AutopilotActorKind `json:"actor_kind"`
	Sequence  int64              `json:"sequence"`
	At        time.Time          `json:"at"`
	Actor     string             `json:"actor"`
	Action    AutopilotAction    `json:"action"`
}

// LocalAction must validate before mutating and cannot perform I/O or schedule
// outward work. Failure rolls back its state, receipt and audit entry together.
// External actions require a separate intent/reconciliation implementation.
type LocalAction struct {
	OwnerAllowed bool
	external     *ExternalAction
	Check        func(Snapshot, ConcreteAction) error
	Apply        func(*Snapshot, ConcreteAction, AutopilotActorKind) (json.RawMessage, uint64, error)
	Undo         func(*Snapshot, AutopilotAction) error
}

type AutopilotCoordinator struct {
	service   *Service
	mu        sync.Mutex
	functions map[string]*AutopilotFunction
	actions   map[string]LocalAction
	admission func() error
	closed    bool
	blocked   bool
	performed func(AutopilotAction)
}

// OnPerformed observes newly committed local effects, never rollback or replay.
// The observer runs after the transaction and coordinator lock are released.
func (c *AutopilotCoordinator) OnPerformed(fn func(AutopilotAction)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.performed = fn
}

type AutopilotFunction struct {
	coordinator *AutopilotCoordinator
	id, version string
	allowed     map[string]bool
	policy      func(Snapshot, ConcreteAction) error
}

func NewAutopilotCoordinator(s *Service, admission func() error) *AutopilotCoordinator {
	c := &AutopilotCoordinator{service: s, functions: map[string]*AutopilotFunction{}, actions: map[string]LocalAction{}, admission: admission}
	c.actions["rename-project"] = projectRenameAction(s)
	c.actions["resolve-choice"] = decisionChoiceAction(s)
	return c
}

// SerializeSettings orders persistence/application against execution admission.
// An unresolved save/application failure closes admission until reconciliation;
// known rejections before persistence preserve the existing admission state.
func (c *AutopilotCoordinator) SerializeSettings(change func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := change()
	if errors.Is(err, config.ErrAutopilotUnchanged) {
		// Rejected edits did not alter either persisted or effective settings.
		// In particular they cannot reconcile an earlier unresolved failure.
		return err
	} else if err != nil {
		c.blocked = true
	} else {
		c.blocked = false
	}
	return err
}

func (c *AutopilotCoordinator) Close() { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true }

func (c *AutopilotCoordinator) RegisterAction(kind string, a LocalAction) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == "" || a.Check == nil || a.Apply == nil {
		return errors.New("local action requires a name, check and apply")
	}
	if _, ok := c.actions[kind]; ok {
		return ErrConflict
	}
	c.actions[kind] = a
	return nil
}

// Register returns trusted authority for a single compiled function. It is not
// an HTTP/model tool and is never registered by saved configuration.
func (c *AutopilotCoordinator) Register(id, version string, kinds []string, policy func(Snapshot, ConcreteAction) error) (*AutopilotFunction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := (autopilot.Settings{}).EffectiveMode(id); err != nil {
		return nil, err
	}
	if c.functions[id] != nil {
		return nil, ErrConflict
	}
	if version == "" || len(kinds) == 0 || policy == nil {
		return nil, errors.New("function requires policy version, actions and policy check")
	}
	allowed := map[string]bool{}
	for _, kind := range kinds {
		if _, ok := c.actions[kind]; !ok || allowed[kind] {
			return nil, errors.New("unknown or duplicate action")
		}
		if c.actions[kind].external != nil && id != autopilot.Operator {
			return nil, errors.New("external actions require operator authority")
		}
		allowed[kind] = true
	}
	f := &AutopilotFunction{c, id, version, allowed, policy}
	c.functions[id] = f
	return f, nil
}

func (c *AutopilotCoordinator) Catalog() []autopilot.Function {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := autopilot.Catalog()
	for i := range out {
		out[i].Available = c.functions[out[i].ID] != nil
	}
	return out
}

func canonicalAction(a ConcreteAction) (ConcreteAction, error) {
	if a.Kind == "" || a.ProjectID == "" || len(a.Args) > 65536 {
		return a, errors.New("action requires kind, project and bounded arguments")
	}
	var args any
	decoder := json.NewDecoder(bytes.NewReader(a.Args))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return a, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return a, errors.New("expected one action argument value")
	}
	data, err := json.Marshal(args)
	a.Args = data
	return a, err
}

func readAutopilotAction(ctx context.Context, conn *sql.Conn, column, id string) (AutopilotAction, error) {
	var a AutopilotAction
	var data string
	// column is selected only by internal callers.
	err := conn.QueryRowContext(ctx, "SELECT payload FROM autopilot_actions WHERE "+column+"=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal([]byte(data), &a)
	return a, err
}

func writeAutopilotAction(ctx context.Context, conn *sql.Conn, a AutopilotAction, actor string, kind AutopilotActorKind, now time.Time) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO autopilot_actions(id,source,project_id,task_id,status,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload`, a.ID, a.Source, a.Action.ProjectID, a.Action.TaskID, a.Status, string(data))
	if err != nil {
		return err
	}
	audit, err := json.Marshal(AutopilotAudit{At: now, Actor: actor, ActorKind: kind, Action: a})
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO autopilot_audit(action_id,project_id,task_id,payload) VALUES(?,?,?,?)", a.ID, a.Action.ProjectID, a.Action.TaskID, string(audit))
	return err
}

func (c *AutopilotCoordinator) check(v Snapshot, a AutopilotAction) error {
	if c.closed || c.blocked {
		return errors.New("autopilot admission is closed")
	}
	if c.admission != nil {
		if err := c.admission(); err != nil {
			return err
		}
	}
	if c.service.upgradeDraining || v.Paused {
		return errors.New("coordination is paused or upgrading")
	}
	f := c.functions[a.Function]
	if f == nil || f.version != a.RuleVersion || !f.allowed[a.Action.Kind] {
		return errors.New("function or action is unavailable or policy changed")
	}
	mode, err := c.service.configuration().Autopilot.EffectiveMode(a.Function)
	if err != nil {
		return err
	}
	if mode == autopilot.Off {
		return errors.New("function is Off")
	}
	p := project(&v, a.Action.ProjectID)
	if p == nil {
		return ErrNotFound
	}
	if p.Paused {
		return errors.New("project is paused")
	}
	if a.Action.TaskID != "" {
		t := task(&v, a.Action.TaskID)
		if t == nil || t.ProjectID != p.ID {
			return errors.New("task target is outside project")
		}
	}
	if a.Function == autopilot.Operator {
		if !p.OperatorPermission.Allowed || p.OperatorPermission.Revision != a.Action.PermissionRevision || p.LandingPaused != nil {
			return errors.New("project operator authority is missing or stale")
		}
	}
	if err := f.policy(v, a.Action); err != nil {
		return err
	}
	return c.actions[a.Action.Kind].Check(v, a.Action)
}

func (f *AutopilotFunction) Submit(ctx context.Context, source, reason string, action ConcreteAction) (AutopilotAction, error) {
	c := f.coordinator
	c.mu.Lock()
	var out AutopilotAction
	notify := false
	newEffect := false
	defer func() {
		fn := c.performed
		c.mu.Unlock()
		if notify && fn != nil {
			fn(out)
		}
	}()
	var runExternal bool
	if c.functions[f.id] != f {
		return out, errors.New("unregistered authority")
	}
	if strings.TrimSpace(source) == "" || len(source) > 256 || strings.TrimSpace(reason) == "" || len(reason) > 8192 {
		return out, errors.New("source and reason are required and bounded")
	}
	a, err := canonicalAction(action)
	if err != nil {
		return out, err
	}
	err = c.service.store.updateTransaction(ctx, func(v *Snapshot, conn *sql.Conn) error {
		prior, err := readAutopilotAction(ctx, conn, "source", source)
		if err == nil {
			if prior.Function != f.id || prior.RuleVersion != f.version || prior.Reason != reason || !reflect.DeepEqual(prior.Action, a) {
				return ErrConflict
			}
			out = prior
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		mode, err := c.service.configuration().Autopilot.EffectiveMode(f.id)
		if err != nil {
			return err
		}
		if mode == autopilot.Off {
			return nil
		}
		cfg := c.service.configuration()
		seated, ok := cfg.Seated()
		if !ok {
			return errors.New("no assistant is seated")
		}
		out = AutopilotAction{ID: uid(), Source: source, Function: f.id, RuleVersion: f.version, Revision: 1, Action: a, Reason: reason, AssistantID: seated.ID, AssistantName: seated.Name, ProposedAt: c.service.now().UTC(), Status: "proposed"}
		if err := c.check(*v, out); err != nil {
			out.Status, out.Detail = "refused", err.Error()
		}
		if err := writeAutopilotAction(ctx, conn, out, out.AssistantID, AutopilotAssistant, c.service.now().UTC()); err != nil {
			return err
		}
		if mode == autopilot.Act && out.Status == "proposed" {
			if err := c.perform(ctx, conn, v, &out, out.AssistantID, AutopilotAssistant); err != nil {
				return err
			}
			runExternal = out.Status == "uncertain"
			newEffect = out.Status == "performed"
		}
		return nil
	})
	if err != nil {
		return AutopilotAction{}, err
	}
	// An existing source returns before perform, so only a new local receipt
	// reaches this notification. External completion has its own lifecycle.
	notify = newEffect
	if runExternal {
		return c.executeExternal(ctx, out)
	}
	return out, nil
}

func (c *AutopilotCoordinator) perform(ctx context.Context, conn *sql.Conn, v *Snapshot, a *AutopilotAction, actor string, kind AutopilotActorKind) error {
	if err := c.check(*v, *a); err != nil {
		a.Status, a.Detail = "refused", err.Error()
	} else if c.actions[a.Action.Kind].external != nil {
		a.ExecutorKind = kind
		a.Status, a.Executor, a.Detail = "uncertain", actor, "Intent recorded; completion is not yet confirmed"
	} else {
		candidate, err := readState(ctx, conn)
		if err != nil {
			return err
		}
		a.ExecutorKind = kind
		inverse, version, err := c.actions[a.Action.Kind].Apply(&candidate, a.Action, kind)
		if err != nil {
			a.Status, a.Detail, a.Executor = "failed", err.Error(), actor
		} else {
			*v = candidate
			a.Status, a.Executor, a.Undo, a.UndoVersion = "performed", actor, inverse, version
		}
	}
	return writeAutopilotAction(ctx, conn, *a, actor, kind, c.service.now().UTC())
}

// OwnerAction binds approval to the exact immutable revision. Overrides use
// ordinary owner target checks and retain the original if replacement fails.
func (c *AutopilotCoordinator) OwnerAction(ctx context.Context, id string, revision uint64, verb string, replacement *ConcreteAction) (AutopilotAction, error) {
	c.mu.Lock()
	var out AutopilotAction
	notify, newEffect := false, false
	defer func() {
		fn := c.performed
		c.mu.Unlock()
		if notify && fn != nil {
			fn(out)
		}
	}()
	var runExternal bool
	var ownerErr error
	if verb != "override" && replacement != nil {
		return out, errors.New("only override accepts replacement arguments")
	}
	err := c.service.store.updateTransaction(ctx, func(v *Snapshot, conn *sql.Conn) error {
		a, err := readAutopilotAction(ctx, conn, "id", id)
		if err != nil {
			return err
		}
		if revision != a.Revision {
			return ErrConflict
		}
		out = a
		switch verb {
		case "cancel":
			if a.Status == "cancelled" {
				return nil
			}
			if a.Status != "proposed" {
				return ErrConflict
			}
			out.Status = "cancelled"
		case "approve":
			if a.Status != "proposed" {
				return nil
			} // replay reads the durable result
			out.Approver = "owner"
			if err := c.perform(ctx, conn, v, &out, "owner", AutopilotOwner); err != nil {
				return err
			}
			runExternal = out.Status == "uncertain"
			newEffect = out.Status == "performed"
			return nil
		case "override":
			if c.closed || c.blocked {
				return errors.New("autopilot admission is closed")
			}
			if c.admission != nil {
				if err := c.admission(); err != nil {
					return err
				}
			}
			if c.service.upgradeDraining {
				return errors.New("upgrade admission is closed")
			}
			if replacement == nil {
				return ErrConflict
			}
			if a.Status != "proposed" {
				if a.Status == "cancelled" {
					prior, err := readAutopilotAction(ctx, conn, "source", "owner-override:"+a.ID)
					candidate, canonicalErr := canonicalAction(*replacement)
					if err == nil && canonicalErr == nil && reflect.DeepEqual(prior.Action, candidate) {
						out = prior
						return nil
					}
				}
				return ErrConflict
			}
			action, err := canonicalAction(*replacement)
			if err != nil {
				return err
			}
			// Replacements cannot turn an ordinary local suggestion into publication.
			if action.Kind != a.Action.Kind || action.ProjectID != a.Action.ProjectID || action.TaskID != a.Action.TaskID {
				return ErrConflict
			}
			adapter, ok := c.actions[action.Kind]
			if !ok || !adapter.OwnerAllowed || adapter.external != nil || a.Function == autopilot.Operator {
				return errors.New("owner replacement requires an ordinary local action")
			}
			candidate, err := readState(ctx, conn)
			if err != nil {
				return err
			}
			replacementRecord := a
			replacementRecord.ID, replacementRecord.Source = uid(), "owner-override-failed:"+uid()
			replacementRecord.Action, replacementRecord.Status, replacementRecord.Approver = action, "failed", "owner"
			failed := func(err error) error {
				ownerErr = err
				replacementRecord.Detail = err.Error()
				out = replacementRecord
				return writeAutopilotAction(ctx, conn, out, "owner", AutopilotOwner, c.service.now().UTC())
			}
			if err := adapter.Check(candidate, action); err != nil {
				return failed(err)
			}
			inverse, version, err := adapter.Apply(&candidate, action, AutopilotOwner)
			if err != nil {
				return failed(err)
			}
			*v = candidate
			// Keep the old immutable proposal and append a separate attributable action.
			out.Status = "cancelled"
			if err := writeAutopilotAction(ctx, conn, out, "owner", AutopilotOwner, c.service.now().UTC()); err != nil {
				return err
			}
			out = a
			out.ID, out.Source, out.Revision = uid(), "owner-override:"+a.ID, a.Revision+1
			out.ExecutorKind = AutopilotOwner
			out.Action, out.Status, out.Approver, out.Executor, out.Undo, out.UndoVersion = action, "performed", "owner", "owner", inverse, version
			newEffect = true
		case "undo":
			if a.Status == "undone" {
				return nil
			}
			adapter, ok := c.actions[a.Action.Kind]
			if a.Status != "performed" || !ok || adapter.Undo == nil || len(a.Undo) == 0 {
				return errors.New("action has no supported undo")
			}
			if c.closed || c.blocked {
				return errors.New("autopilot admission is closed")
			}
			if c.service.upgradeDraining {
				return errors.New("upgrade admission is closed")
			}
			if c.admission != nil {
				if err := c.admission(); err != nil {
					return err
				}
			}
			if err := adapter.Undo(v, a); err != nil {
				return err
			}
			out.Status = "undone"
		default:
			return errors.New("unknown owner action")
		}
		return writeAutopilotAction(ctx, conn, out, "owner", AutopilotOwner, c.service.now().UTC())
	})
	if err != nil {
		return AutopilotAction{}, err
	}
	if ownerErr != nil {
		return out, ownerErr
	}
	notify = newEffect
	if runExternal {
		return c.executeExternal(ctx, out)
	}
	return out, nil
}

type RenameProjectArgs struct {
	Title string `json:"title"`
}
type renameInverse struct {
	Title   string `json:"title"`
	Renamed bool   `json:"renamed"`
}

func projectRenameAction(s *Service) LocalAction {
	check := func(v Snapshot, a ConcreteAction) error {
		if a.TaskID != "" || a.Commit != "" {
			return errors.New("rename target must be a project")
		}
		p := project(&v, a.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		if p.TitleRevision != a.TargetVersion {
			return ErrConflict
		}
		var args RenameProjectArgs
		if err := strictJSON(a.Args, &args); err != nil {
			return err
		}
		_, err := cleanProjectTitle(args.Title)
		return err
	}
	return LocalAction{OwnerAllowed: true, Check: check, Apply: func(v *Snapshot, a ConcreteAction, _ AutopilotActorKind) (json.RawMessage, uint64, error) {
		if err := check(*v, a); err != nil {
			return nil, 0, err
		}
		p := project(v, a.ProjectID)
		inverse, _ := json.Marshal(renameInverse{p.Title, p.TitleRenamed})
		var args RenameProjectArgs
		_ = json.Unmarshal(a.Args, &args)
		renameProject(v, p, strings.TrimSpace(args.Title), s.now().UTC())
		return inverse, p.TitleRevision, nil
	}, Undo: func(v *Snapshot, a AutopilotAction) error {
		p := project(v, a.Action.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		if p.TitleRevision != a.UndoVersion {
			return ErrConflict
		}
		var inverse renameInverse
		if err := json.Unmarshal(a.Undo, &inverse); err != nil {
			return err
		}
		p.Title, p.TitleRenamed = inverse.Title, inverse.Renamed
		p.TitleRevision++
		p.UpdatedAt = s.now().UTC()
		record(v, p.UpdatedAt, p.ID, "project.renamed", "Undid autopilot rename")
		return nil
	}}
}
