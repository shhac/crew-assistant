package core

import (
	"testing"
)

func TestBriefChangesAreVersioned(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	if p.Brief.Version != 1 {
		t.Fatalf("new brief version %d", p.Brief.Version)
	}
	updated, err := s.UpdateBrief(testContext, p.ID, BriefInput{Goal: "A sharper outcome", Criteria: []string{" one ", "", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Brief.Version != 2 || len(updated.Brief.Criteria) != 2 || updated.Brief.Criteria[0] != "one" {
		t.Fatalf("brief %+v", updated.Brief)
	}
	if _, err = s.UpdateBrief(testContext, p.ID, BriefInput{}); err == nil {
		t.Fatal("brief without a goal accepted")
	}
}

func TestPlaybookValidation(t *testing.T) {
	good := Templates["draft"]
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Playbook){
		"no reviewer": func(p *Playbook) { p.Roles = p.Roles[:1] },
		"two writers": func(p *Playbook) {
			p.Roles = append(p.Roles, Role{Name: "Second", Kind: RoleImplementer, Engine: "codex"})
		},
		"unknown engine":     func(p *Playbook) { p.Roles[1].Engine = "other" },
		"duplicate names":    func(p *Playbook) { p.Roles[1].Name = p.Roles[0].Name },
		"no rounds":          func(p *Playbook) { p.MaxRounds = 0 },
		"unsupported medium": func(p *Playbook) { p.Medium = "git" },
		"unattended deliver": func(p *Playbook) { p.Deliver = "assistant" },
		"relative folder":    func(p *Playbook) { p.DeliverTo = "drafts" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := Templates["draft"]
			p.Roles = append([]Role(nil), p.Roles...)
			mutate(&p)
			if p.Validate() == nil {
				t.Fatal("accepted")
			}
		})
	}
	if Templates["draft"].Roles[1].Engine != "codex" {
		t.Fatal("a test mutated the shared template")
	}
}

func TestTasksNeedABriefAndAPlaybook(t *testing.T) {
	s, _ := fixture(t)
	bare, err := s.CreateProject(testContext, ProjectInput{Title: "Bare"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueTask(testContext, bare.ID, TaskInput{Objective: "Draft it"}); err == nil {
		t.Fatal("task queued without a playbook")
	}
	if _, err = s.SetPlaybook(testContext, bare.ID, Templates["draft"]); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueTask(testContext, bare.ID, TaskInput{Objective: "Draft it"}); err == nil {
		t.Fatal("task queued without a brief")
	}
	p := newProject(t, s)
	if _, err = s.QueueTask(testContext, p.ID, TaskInput{}); err == nil {
		t.Fatal("task without an objective accepted")
	}
}

func TestOneTaskRunsAtATimeWithRolesCopiedAtStart(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	first, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "First"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueTask(testContext, p.ID, TaskInput{Objective: "Second"}); err != nil {
		t.Fatal(err)
	}
	started, ok, err := s.NextTask(testContext)
	if err != nil || !ok || started.ID != first.ID || started.Status != TaskWriting || started.Round != 1 || len(started.Roles) != 2 || started.MaxRounds != 3 {
		t.Fatalf("started %+v ok=%v err=%v", started, ok, err)
	}
	// A playbook change after start does not reshape the running task.
	changed := Templates["draft"]
	changed.Roles = append([]Role(nil), changed.Roles...)
	changed.Roles[0].Engine = "codex"
	changed.MaxRounds = 5
	if _, err = s.SetPlaybook(testContext, p.ID, changed); err != nil {
		t.Fatal(err)
	}
	again, ok, err := s.NextTask(testContext)
	if err != nil || !ok || again.ID != first.ID || again.Roles[0].Engine != "claude" || again.MaxRounds != 3 {
		t.Fatalf("the running task changed or a second started: %+v", again)
	}
	if _, err = s.OpenTaskDecision(testContext, first.ID, "delivery", DecisionInput{Title: "Approve?", Context: "Draft ready", Recommendation: "Approve", Choices: []string{"Approve", "Request changes"}}); err != nil {
		t.Fatal(err)
	}
	next, ok, err := s.NextTask(testContext)
	if err != nil || !ok || next.Objective != "Second" || next.Roles[0].Engine != "codex" || next.MaxRounds != 5 {
		t.Fatalf("waiting task should free the loop for the next, got %+v", next)
	}
	v, _ := s.Snapshot(testContext)
	held := v.Tasks[0]
	if held.Status != TaskWaiting || held.DecisionID == "" {
		t.Fatalf("decision did not hold its task: %+v", held)
	}
	for _, d := range v.Decisions {
		if d.ID == held.DecisionID && (d.TaskID != first.ID || d.Kind != "delivery" || d.ProjectID != p.ID) {
			t.Fatalf("decision not tied to its task: %+v", d)
		}
	}
}
