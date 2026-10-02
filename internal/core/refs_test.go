package core

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestADefaultPrefixComesFromTheTitle(t *testing.T) {
	for title, want := range map[string]string{
		"crew-assistant":   "CA",
		"crewAssistant":    "CA",
		"Q4 planning memo": "QPM",
		"Reading list":     "RL",
		"dotfiles":         "DOT",
		"go":               "GO",
		"2026 budget plan": "BP",
		"a b c d e f g h":  "ABCDEF",
		"":                 "P",
		"—":                "P",
	} {
		if got := defaultPrefix(title); got != want {
			t.Errorf("defaultPrefix(%q) = %q, want %q", title, got, want)
		}
		if _, err := CleanPrefix(defaultPrefix(title)); err != nil {
			t.Errorf("defaultPrefix(%q) = %q is not a valid prefix", title, defaultPrefix(title))
		}
	}
}

func TestADefaultPrefixThatIsTakenGetsMoreOfTheTitle(t *testing.T) {
	v := &Snapshot{}
	var got []string
	for range 4 {
		prefix := uniquePrefix(v, "crew-assistant")
		got = append(got, prefix)
		v.Projects = append(v.Projects, Project{ID: prefix, Prefix: prefix})
	}
	if want := []string{"CA", "CAR", "CAE", "CAW"}; !slices.Equal(got, want) {
		t.Fatalf("prefixes %v, want %v", got, want)
	}
	v = &Snapshot{Projects: []Project{{ID: "a", Prefix: "c"}, {ID: "b", Prefix: "C2"}}}
	if got := uniquePrefix(v, "C"); got != "C3" {
		t.Fatalf("a title with no more letters got %q", got)
	}
}

func TestTasksAreNumberedPerProjectInTheOrderTheyWereAskedFor(t *testing.T) {
	s, _ := fixture(t)
	crew, err := s.CreateProject(testContext, ProjectInput{Title: "crew-assistant", Brief: BriefInput{Goal: "Help"}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateProject(testContext, ProjectInput{Title: "Crew archive", Brief: BriefInput{Goal: "Keep"}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if crew.Prefix != "CA" || other.Prefix == "CA" || other.Prefix == "" {
		t.Fatalf("prefixes %q and %q", crew.Prefix, other.Prefix)
	}
	var refs []string
	for _, p := range []Project{crew, other, crew, crew, other} {
		task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Work"})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, task.Ref)
	}
	if want := []string{"CA-1", other.Prefix + "-1", "CA-2", "CA-3", other.Prefix + "-2"}; !slices.Equal(refs, want) {
		t.Fatalf("readable IDs %v, want %v", refs, want)
	}
	// Reordering the to-do list doesn't renumber anything.
	snap, _ := s.Snapshot(testContext)
	var queued []string
	for _, task := range snap.Tasks {
		if task.ProjectID == crew.ID {
			queued = append([]string{task.ID}, queued...)
		}
	}
	if _, err := s.OrderTasks(testContext, crew.ID, queued, OrderedByOwner); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	for _, task := range snap.Tasks {
		if task.ProjectID == crew.ID && task.Ref != crew.TaskRef(task.Number) {
			t.Fatalf("task %+v", task)
		}
	}
	if snap.Tasks[0].Ref != "CA-3" {
		t.Fatalf("the reordered list starts with %s", snap.Tasks[0].Ref)
	}
}

func TestANumberIsNeverGivenTwice(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	for range 3 {
		if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Work"}); err != nil {
			t.Fatal(err)
		}
	}
	// Tasks are never deleted, but even state missing the latest one must
	// not hand its number out again.
	if err := s.store.update(testContext, func(v *Snapshot) error {
		v.Tasks = v.Tasks[:2]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "More"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Number != 4 {
		t.Fatalf("the next task is number %d, want 4", next.Number)
	}
}

// State written before readable IDs has no prefixes or numbers; opening it
// gives them once, by when each task was asked for.
func TestOpeningOlderStateNumbersItsTasksOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Tasks listed in to-do order, not the order they were asked for; the
	// last two were asked for at the same moment.
	payload := `{"schema":2,"snapshot":{
		"projects":[
			{"id":"p1","title":"crew-assistant","status":"active","directories":[]},
			{"id":"p2","title":"Crew archive","status":"active","directories":[]},
			{"id":"p3","title":"cabinet","status":"active","prefix":"CA","next_task":8,"directories":[]}
		],
		"tasks":[
			{"id":"t3","project_id":"p1","objective":"Third","status":"queued","created_at":"2026-09-03T00:00:00Z"},
			{"id":"t1","project_id":"p1","objective":"First","status":"landed","created_at":"2026-09-01T00:00:00Z"},
			{"id":"u1","project_id":"p2","objective":"Other","status":"queued","created_at":"2026-09-02T00:00:00Z"},
			{"id":"t2","project_id":"p1","objective":"Second","status":"queued","created_at":"2026-09-02T00:00:00Z"},
			{"id":"t4","project_id":"p1","objective":"Fourth","status":"queued","created_at":"2026-09-04T00:00:00Z"},
			{"id":"t5","project_id":"p1","objective":"Fifth","status":"queued","created_at":"2026-09-04T00:00:00Z"},
			{"id":"v1","project_id":"p3","objective":"Kept","status":"queued","number":7,"created_at":"2026-09-01T00:00:00Z"}
		]}}`
	if _, err = st.db.Exec("INSERT INTO state(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", payload); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	prefixes := map[string]string{}
	for _, p := range first.Projects {
		prefixes[p.ID] = p.Prefix
	}
	// p3 already had CA, so crew-assistant gets another prefix, and the
	// archive one that neither has.
	if prefixes["p3"] != "CA" || prefixes["p1"] == "CA" || prefixes["p1"] == "" || prefixes["p2"] == "" || prefixes["p1"] == prefixes["p2"] {
		t.Fatalf("prefixes %v", prefixes)
	}
	refs := map[string]string{}
	for _, task := range first.Tasks {
		refs[task.ID] = task.Ref
	}
	p1 := prefixes["p1"]
	want := map[string]string{"t1": p1 + "-1", "t2": p1 + "-2", "t3": p1 + "-3", "t4": p1 + "-4", "t5": p1 + "-5", "u1": prefixes["p2"] + "-1", "v1": "CA-7"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("readable IDs %v, want %v", refs, want)
	}
	if first.Projects[0].NextTask != 6 || first.Projects[2].NextTask != 8 {
		t.Fatalf("next numbers %d and %d", first.Projects[0].NextTask, first.Projects[2].NextTask)
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	again, err := st.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Projects, first.Projects) || !reflect.DeepEqual(again.Tasks, first.Tasks) {
		t.Fatal("opening the state again changed its prefixes or numbers")
	}
}

func TestRenamingAPrefixRenamesEveryTasksReadableIDAndKeepsItsLinks(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api := tasks[0], tasks[1]
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: RelationDependsOn, Other: schema.ID, By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.SetProjectPrefix(testContext, p.ID, " crew ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Prefix != "CREW" {
		t.Fatalf("prefix %q", renamed.Prefix)
	}
	snap, _ := s.Snapshot(testContext)
	for i, task := range snap.Tasks {
		if want := renamed.TaskRef(i + 1); task.Ref != want {
			t.Fatalf("task %d reads as %q, want %q", i, task.Ref, want)
		}
	}
	if got := taskByID(t, s, api.ID); !slices.Equal(got.DependsOn, []string{schema.ID}) || !slices.Equal(got.WaitsFor, []string{"Schema"}) {
		t.Fatalf("api after the rename %+v", got)
	}
	if found, ok := snap.FindTask("crew-2"); !ok || found.ID != api.ID {
		t.Fatalf("crew-2 found %+v", found)
	}
	if _, ok := snap.FindTask("FP-2"); ok {
		t.Fatal("the old readable ID still resolves")
	}
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool {
		return a.Kind == "project.prefix_updated" && a.ProjectID == p.ID && a.Summary == "Task IDs now start CREW- instead of FP-"
	}) {
		t.Fatalf("activity %+v", snap.Activity)
	}
}

func TestAPrefixMustBeValidAndUnique(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	other, err := s.CreateProject(testContext, ProjectInput{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "1A", "A-B", "TOOLONG", "ÄB"} {
		if _, err := s.SetProjectPrefix(testContext, p.ID, bad); !errors.Is(err, ErrPrefix) {
			t.Errorf("prefix %q: %v", bad, err)
		}
	}
	if _, err := s.SetProjectPrefix(testContext, p.ID, other.Prefix); !errors.Is(err, ErrConflict) {
		t.Fatalf("took another project's prefix: %v", err)
	}
	if _, err := s.SetProjectPrefix(testContext, p.ID, "ot2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectPrefix(testContext, other.ID, "OT2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("prefixes are compared in any case: %v", err)
	}
	if _, err := s.SetProjectPrefix(testContext, "missing", "NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing project: %v", err)
	}
}

func TestRenamingAProjectChangesOnlyItsTitle(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api := tasks[0], tasks[1]
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: api.ID, Relation: RelationDependsOn, Other: schema.ID, By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(testContext)
	renamed, err := s.SetProjectTitle(testContext, p.ID, "  Crew rebuild  ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Crew rebuild" || !renamed.TitleRenamed || renamed.ID != p.ID || renamed.Prefix != p.Prefix || renamed.NextTask != 4 || renamed.ScratchDirectory != p.ScratchDirectory || !slices.Equal(renamed.Directories, p.Directories) {
		t.Fatalf("renamed %+v from %+v", renamed, p)
	}
	snap, _ := s.Snapshot(testContext)
	// Wait pointers carry the current project title, rather than a stored copy.
	for i := range before.Tasks {
		for j := range before.Tasks[i].WaitingOn {
			before.Tasks[i].WaitingOn[j].Project = renamed.Title
		}
	}
	if !reflect.DeepEqual(snap.Tasks, before.Tasks) {
		t.Fatalf("tasks changed: %+v", snap.Tasks)
	}
	if got := taskByID(t, s, api.ID); got.Ref != "FP-2" || !slices.Equal(got.DependsOn, []string{schema.ID}) {
		t.Fatalf("api after the rename %+v", got)
	}
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool {
		return a.Kind == "project.renamed" && a.ProjectID == p.ID && a.Summary == "Renamed from “Fictional project” to “Crew rebuild”"
	}) {
		t.Fatalf("activity %+v", snap.Activity)
	}
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool { return a.Kind == "project.created" && a.Summary == "Fictional project" }) {
		t.Fatal("the rename rewrote history")
	}
	if _, err := s.SetProjectTitle(testContext, p.ID, "Crew rebuild"); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Snapshot(testContext)
	if len(again.Activity) != len(snap.Activity) {
		t.Fatal("an unchanged title was recorded as a rename")
	}
}

func TestAProjectNameMustBeGivenAndNotTooLong(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	for bad, message := range map[string]string{
		"":                       "a project needs a name",
		"   ":                    "a project needs a name",
		strings.Repeat("é", 201): "a project name can be at most 200 characters",
	} {
		if _, err := s.SetProjectTitle(testContext, p.ID, bad); err == nil || err.Error() != message {
			t.Errorf("title %q: %v", bad, err)
		}
	}
	if got, _ := s.Snapshot(testContext); got.Projects[0].Title != p.Title || got.Projects[0].TitleRenamed {
		t.Fatalf("a refused rename changed the project %+v", got.Projects[0])
	}
	longest := strings.Repeat("é", 200)
	if renamed, err := s.SetProjectTitle(testContext, p.ID, longest); err != nil || renamed.Title != longest {
		t.Fatalf("the longest name: %v", err)
	}
	if _, err := s.SetProjectTitle(testContext, "missing", "Name"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing project: %v", err)
	}
}

func TestEitherIDNamesATaskAndOnlyTheCanonicalOneIsKept(t *testing.T) {
	s, p, tasks := linkedProject(t)
	schema, api, dashboard := tasks[0], tasks[1], tasks[2]
	if schema.Ref != p.Prefix+"-1" {
		t.Fatalf("schema reads as %q", schema.Ref)
	}
	snap, _ := s.Snapshot(testContext)
	for _, ref := range []string{schema.ID, schema.Ref, " " + schema.Ref + " ", strings.ToLower(schema.Ref)} {
		if found, ok := snap.FindTask(ref); !ok || found.ID != schema.ID {
			t.Errorf("%q found %+v", ref, found)
		}
	}
	// The CLI resolves against the state as the daemon serves it.
	var served Snapshot
	if data, err := json.Marshal(snap); err != nil || json.Unmarshal(data, &served) != nil {
		t.Fatal(err)
	}
	if found, ok := served.FindTask(strings.ToLower(api.Ref)); !ok || found.ID != api.ID || found.Ref != api.Ref {
		t.Fatalf("the served state found %+v", found)
	}
	for _, ref := range []string{p.Prefix + "-9", p.Prefix + "-0", p.Prefix + "-x", "ZZ-1", "-1", ""} {
		if _, ok := snap.FindTask(ref); ok {
			t.Errorf("%q found a task", ref)
		}
	}
	// Links, dependencies, the order and wakes named by readable IDs keep
	// canonical ones.
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: api.Ref, Relation: RelationDependsOn, Other: strings.ToLower(schema.Ref), By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkTasks(testContext, Link{Project: p.ID, Task: dashboard.Ref, Relation: RelationRelatesTo, Other: schema.Ref, By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	queued, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Docs", DependsOn: []string{dashboard.Ref, dashboard.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(queued.DependsOn, []string{dashboard.ID}) {
		t.Fatalf("docs depends on %v", queued.DependsOn)
	}
	if got := taskByID(t, s, api.ID); !slices.Equal(got.DependsOn, []string{schema.ID}) {
		t.Fatalf("api depends on %v", got.DependsOn)
	}
	if got := taskByID(t, s, dashboard.ID); !slices.Equal(got.RelatesTo, []string{schema.ID}) {
		t.Fatalf("dashboard relates to %v", got.RelatesTo)
	}
	if _, err := s.UnlinkTasks(testContext, Link{Project: p.ID, Task: api.Ref, Other: schema.ID, By: LinkedByOwner}); err != nil {
		t.Fatal(err)
	}
	ordered, err := s.OrderTasks(testContext, p.ID, []string{queued.Ref, dashboard.Ref, api.ID, schema.Ref}, OrderedByOwner)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].ID != queued.ID || ordered[3].ID != schema.ID {
		t.Fatalf("order %+v", ordered)
	}
	wake, err := s.RegisterWake(testContext, WakeInput{Owner: WakeAssistant, On: WakeOnTask, Target: strings.ToLower(api.Ref)})
	if err != nil {
		t.Fatal(err)
	}
	if wake.Target != api.ID {
		t.Fatalf("the wake waits on %q", wake.Target)
	}
	if _, err := s.ApplyPM(testContext, p.ID, PMAnswer{Depends: map[string][]string{api.Ref: {schema.Ref}}}); err != nil {
		t.Fatal(err)
	}
	if got := taskByID(t, s, api.ID); !slices.Equal(got.DependsOn, []string{schema.ID}) {
		t.Fatalf("after the PM, api depends on %v", got.DependsOn)
	}
}

func TestActivityAboutATaskNamesIt(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	for _, a := range snap.Activity {
		if (a.Kind == "task.queued") != (a.TaskID == task.ID) {
			t.Fatalf("activity %+v", a)
		}
	}
}

func TestAReadableIDIsNeverStored(t *testing.T) {
	s, p, tasks := linkedProject(t)
	var payload string
	if err := s.store.db.QueryRow("SELECT payload FROM state WHERE id=1").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Snapshot struct {
			Tasks []map[string]any `json:"tasks"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(payload), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Snapshot.Tasks) != len(tasks) {
		t.Fatalf("stored %d tasks", len(stored.Snapshot.Tasks))
	}
	for i, task := range stored.Snapshot.Tasks {
		if _, ok := task["ref"]; ok || task["number"] != float64(i+1) {
			t.Fatalf("stored task %v", task)
		}
	}
	// It is still derived for everyone who reads the state.
	snap, _ := s.Snapshot(testContext)
	if snap.Tasks[0].Ref != p.Prefix+"-1" {
		t.Fatalf("the first task reads as %q", snap.Tasks[0].Ref)
	}
}

func TestATasksWakeNamedByItsReadableIDKeepsTheCanonicalOne(t *testing.T) {
	s, _, tasks := linkedProject(t)
	api := tasks[1]
	wake, err := s.RegisterWake(testContext, WakeInput{Owner: WakeTask, TaskID: strings.ToLower(api.Ref), On: WakeOnTime, Target: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if wake.TaskID != api.ID {
		t.Fatalf("the wake belongs to %q", wake.TaskID)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Wakes) != 1 || snap.Wakes[0].TaskID != api.ID {
		t.Fatalf("stored wakes %+v", snap.Wakes)
	}
	if _, err := s.CancelWake(testContext, wake.ID, api.ID); err != nil {
		t.Fatalf("the task could not cancel its own wake: %v", err)
	}
	if _, err := s.RegisterWake(testContext, WakeInput{Owner: WakeTask, TaskID: "ZZ-1", On: WakeOnTime, Target: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a wake for a missing task: %v", err)
	}
}
