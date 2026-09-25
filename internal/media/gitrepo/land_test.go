package gitrepo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A change lands as one commit of its own: the approved tree on top of what
// the target held, with none of the task's drafts in the target's history.
func TestAChangeLandsAsOneCommitWithItsDraftsLeftBehind(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil, SignNever)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := r.Begin(ctx, "crew-task/a", "main")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main // draft 1\n")
	draft1, _, err := r.Snapshot(ctx, base, base, "draft 1")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main // draft 2\n")
	draft2, _, err := r.Snapshot(ctx, base, draft1, "draft 2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Fetch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	landed, err := r.PushSquashed(ctx, "crew-task/a", draft2, "main", "Add A\n\nCrew-Task: a revision 2")
	if err != nil {
		t.Fatal(err)
	}
	if git(t, source, "rev-parse", "main") != landed || git(t, source, "rev-parse", "main^") != base {
		t.Fatal("main should be one new commit on what it held")
	}
	if git(t, source, "rev-parse", "main^{tree}") != git(t, r.Workspace(), "rev-parse", draft2+"^{tree}") {
		t.Fatal("the landed commit does not hold the approved tree")
	}
	if log := git(t, source, "log", "--format=%s", "main"); strings.Contains(log, "draft") {
		t.Fatalf("drafts reached main:\n%s", log)
	}
	if tip, _ := r.Fetch(ctx, "main"); tip != landed {
		t.Fatal("the landing was not fetched back")
	}
	if found, err := r.Mentions(ctx, landed, "Crew-Task: a revision 2"); err != nil || !found {
		t.Fatalf("the landing's trailer was not found: %v", err)
	}
	if found, _ := r.Mentions(ctx, landed, "Crew-Task: b revision 1"); found {
		t.Fatal("another task's trailer was found")
	}
	// The drafts are not in main, so the branch as it stands is behind it
	// and pushing it again is refused rather than landing twice.
	if again, err := r.PushSquashed(ctx, "crew-task/a", draft2, "main", "Add A"); !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("the same branch landed again: %s %v", again, err)
	}
}

// A change behind the target is refused as a moved target, to catch up
// first, and one already there adds nothing.
func TestASquashedLandingNeedsTheChangeCaughtUp(t *testing.T) {
	source := ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil, SignNever)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := r.Begin(ctx, "crew-task/a", "main")
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	change, _, _ := r.Snapshot(ctx, base, base, "a")
	write(t, filepath.Join(source, "owner.go"), "package main\n")
	git(t, source, "add", "owner.go")
	git(t, source, "commit", "-q", "-m", "owner work")
	ownerTip := git(t, source, "rev-parse", "main")
	if _, err = r.Fetch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.PushSquashed(ctx, "crew-task/a", change, "main", "Add A"); !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("a change behind main was landed: %v", err)
	}
	if git(t, source, "rev-parse", "main") != ownerTip {
		t.Fatal("main moved")
	}
	// Caught up to exactly main's content, it adds nothing.
	if err = r.Reset(ctx, "crew-task/a", ownerTip); err != nil {
		t.Fatal(err)
	}
	if landed, err := r.PushSquashed(ctx, "crew-task/a", ownerTip, "main", "Nothing"); err != nil || landed != ownerTip {
		t.Fatalf("a change already there should land as main itself: %s %v", landed, err)
	}
}

// After the target's history is rewritten, catching up replays only the
// task's own change: what the target dropped stays dropped, and a replay
// that conflicts is left for the implementer, who cannot record markers.
func TestCatchingUpAfterARewriteKeepsOnlyTheTasksOwnChange(t *testing.T) {
	source := ownerRepo(t)
	write(t, filepath.Join(source, "dropped.go"), "package main // about to be dropped\n")
	git(t, source, "add", "dropped.go")
	git(t, source, "commit", "-q", "-m", "a commit the owner will drop")
	r, err := Open(ctx, t.TempDir(), source, nil, SignNever)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := r.Begin(ctx, "crew-task/a", "main")
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main\n")
	change, _, _ := r.Snapshot(ctx, base, base, "a")
	// The owner rewrites main without the dropped commit.
	git(t, source, "reset", "-q", "--hard", "HEAD~1")
	write(t, filepath.Join(source, "owner.go"), "package main\n")
	git(t, source, "add", "owner.go")
	git(t, source, "commit", "-q", "-m", "owner work")
	tip, err := r.Fetch(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if merged, _ := r.MergeClean(ctx, change, tip, "merge"); merged != "" {
		files := git(t, r.Workspace(), "ls-tree", "-r", "--name-only", merged)
		if !strings.Contains(files, "dropped.go") {
			t.Fatal("expected a merge to bring the dropped file back, which is why a rewrite replays")
		}
	}
	replayed, err := r.ReplayClean(ctx, base, change, tip, "catch up")
	if err != nil || replayed == "" {
		t.Fatalf("replay: %q %v", replayed, err)
	}
	files := git(t, r.Workspace(), "ls-tree", "-r", "--name-only", replayed)
	if strings.Contains(files, "dropped.go") || !strings.Contains(files, "a.go") || !strings.Contains(files, "owner.go") {
		t.Fatalf("the replay should hold the task's change on the rewritten main, and nothing it dropped:\n%s", files)
	}
	if git(t, r.Workspace(), "rev-parse", replayed+"^") != tip {
		t.Fatal("the replay should sit on the rewritten main")
	}

	// A replay that conflicts is left for the implementer.
	write(t, filepath.Join(source, "a.go"), "package main // the owner's own a.go\n")
	git(t, source, "add", "a.go")
	git(t, source, "commit", "-q", "-m", "owner a.go")
	tip, _ = r.Fetch(ctx, "main")
	if replayed, _ := r.ReplayClean(ctx, base, change, tip, "catch up"); replayed != "" {
		t.Fatal("a conflicting replay was recorded")
	}
	conflicts, err := r.Replay(ctx, "crew-task/a", base, change, tip)
	if err != nil || len(conflicts) != 1 || conflicts[0] != "a.go" {
		t.Fatalf("conflicts %v %v", conflicts, err)
	}
	if _, _, err = r.Snapshot(ctx, tip, change, "unresolved"); err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("an unresolved replay was recorded: %v", err)
	}
	write(t, filepath.Join(r.Workspace(), "a.go"), "package main // both\n")
	resolved, _, err := r.Snapshot(ctx, tip, change, "resolved")
	if err != nil || git(t, r.Workspace(), "rev-parse", resolved+"^") != tip {
		t.Fatalf("the resolved replay should be one commit on main: %v", err)
	}
	if strings.Contains(git(t, r.Workspace(), "ls-tree", "-r", "--name-only", resolved), "dropped.go") {
		t.Fatal("the dropped file came back")
	}
	if _, err := os.Stat(filepath.Join(r.Workspace(), ".git", replayMarker)); !os.IsNotExist(err) {
		t.Fatal("the replay marker was left behind")
	}
}
