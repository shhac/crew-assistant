package core

import (
	"strings"
	"testing"
)

func TestRepositoryScopedCheckValidation(t *testing.T) {
	for _, tc := range []struct {
		full, scoped string
		valid        bool
	}{
		{"", "", true}, {"make check", "make check-scoped", true},
		{"", "make check-scoped", false}, {"make check", "a\nb", false},
		{"make check", strings.Repeat("a", 501), false},
	} {
		r := RepositoryInput{Check: tc.full, ScopedCheck: tc.scoped}.repository("id", t.TempDir())
		if (r.validate() == nil) != tc.valid {
			t.Fatalf("%+v", tc)
		}
		if r.ScopedCheck != tc.scoped {
			t.Fatal("scoped command lost")
		}
	}
}

func TestFullCheckBoundToDraftAndRequirements(t *testing.T) {
	task := Task{TextVersion: 2, Revisions: []Revision{{N: 1, Ref: "draft"}}, FullCheck: &MergeValidation{Revision: 1, Ref: "draft", BriefVersion: 3, TextVersion: 2}}
	if !task.FullCheckPending(3) || task.FullCheckPassed(3) {
		t.Fatal(task)
	}
	task.FullCheck.Checked = true
	if !task.FullCheckPassed(3) || task.FullCheckPending(3) {
		t.Fatal(task)
	}
	task.FullCheck.Failure = "required skip"
	if task.FullCheckPassed(3) {
		t.Fatal("failure passed")
	}
	task.FullCheck.Failure = ""
	for _, mutate := range []func(*Task){
		func(t *Task) { t.TextVersion++ },
		func(t *Task) { t.Revisions = []Revision{{N: 2, Ref: "draft"}} },
		func(t *Task) { t.Revisions = []Revision{{N: 1, Ref: "other"}} },
	} {
		copy := task
		mutate(&copy)
		if copy.FullCheckCurrent(3) || copy.FullCheckPassed(3) {
			t.Fatal(copy)
		}
	}
	if task.FullCheckCurrent(4) {
		t.Fatal("stale brief")
	}
	task.NextRound()
	if task.FullCheck != nil {
		t.Fatal("next round retained gate")
	}
}

func TestScopedChecksRequireGitMedium(t *testing.T) {
	pb := Templates["draft"]
	pb.Check = "full"
	pb.ScopedCheck = "scoped"
	if err := pb.Validate(); err == nil {
		t.Fatal("document playbook accepted a scoped repository check")
	}
}
