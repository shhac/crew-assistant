//go:build !windows

package gitrepo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// delivery is a project on a fresh owner's repository with one recorded
// revision, and the commit it was built on.
func delivery(t *testing.T) (r Repo, source, base, commit string) {
	t.Helper()
	source = ownerRepo(t)
	r, err := Open(ctx, t.TempDir(), source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err = r.Begin(ctx, "crew/x", "main")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), "feature.go"), "package main\n")
	commit, _, err = r.Snapshot(ctx, base, base, "draft 1")
	if err != nil {
		t.Fatal(err)
	}
	return r, source, base, commit
}

func TestDeliveredFindsTheBranchDeliverChose(t *testing.T) {
	r, source, _, commit := delivery(t)
	if name, ok, err := r.Delivered(ctx, commit, "paul/x"); err != nil || ok {
		t.Fatalf("found a delivery before any was made: %q %v %v", name, ok, err)
	}
	// The owner already has a branch by that name, at another commit.
	git(t, source, "branch", "paul/x", "main")
	delivered, err := r.Deliver(ctx, commit, "paul/x")
	if err != nil || delivered != "paul/x-2" {
		t.Fatalf("delivered %q: %v", delivered, err)
	}
	if name, ok, err := r.Delivered(ctx, commit, "paul/x"); err != nil || !ok || name != delivered {
		t.Fatalf("Delivered says %q %v %v, Deliver chose %q", name, ok, err, delivered)
	}
	// The owner checks the delivered branch out: a retried delivery settles on
	// it rather than making another, and both still agree on its name.
	git(t, source, "checkout", "-q", delivered)
	again, err := r.Deliver(ctx, commit, "paul/x")
	if err != nil || again != delivered {
		t.Fatalf("a retried delivery chose %q rather than %q: %v", again, delivered, err)
	}
	if out := git(t, source, "branch", "--list", "paul/x-3"); out != "" {
		t.Fatalf("a retried delivery made another branch: %s", out)
	}
	if name, ok, err := r.Delivered(ctx, commit, "paul/x"); err != nil || !ok || name != delivered {
		t.Fatalf("Delivered says %q %v %v, Deliver chose %q", name, ok, err, delivered)
	}
}

func TestAPushIntoALinkedWorktreeLandsOnlyWhenItIsClean(t *testing.T) {
	r, source, base, commit := delivery(t)
	git(t, source, "branch", "target", "main")
	linked := filepath.Join(t.TempDir(), "linked")
	if err := AddWorktree(ctx, source, linked, "target"); err != nil {
		t.Fatal(err)
	}
	edit := filepath.Join(linked, "main.go")
	write(t, edit, "package main // the owner's edit\n")
	if err := r.PushFastForward(ctx, commit, "target"); !errors.Is(err, ErrDirtyCheckout) {
		t.Fatalf("landed over the owner's uncommitted work in a linked worktree: %v", err)
	}
	if raw, _ := os.ReadFile(edit); string(raw) != "package main // the owner's edit\n" {
		t.Fatal("the owner's uncommitted work was changed")
	}
	if git(t, source, "rev-parse", "target") != base {
		t.Fatal("the branch moved though its checkout is dirty")
	}
	// ErrCheckedOut is never the answer here: the push lets git update a
	// clean checkout in place, linked worktrees included, so with this git a
	// checked-out target only ever refuses as dirty. ErrCheckedOut is left for
	// a git whose receive-pack refuses updateInstead outright.
	git(t, linked, "checkout", "--", "main.go")
	if err := r.PushFastForward(ctx, commit, "target"); err != nil {
		t.Fatalf("a clean linked worktree refused the landing: %v", err)
	}
	if git(t, source, "rev-parse", "target") != commit {
		t.Fatal("target did not land on the change")
	}
	if _, err := os.Stat(filepath.Join(linked, "feature.go")); err != nil {
		t.Fatal("the clean linked worktree was not brought up to date")
	}
}

// A read failure is never proof that an interrupted delivery went nowhere.
func TestDeliveryReadDistinguishesAbsentFromUnknown(t *testing.T) {
	for _, failure := range []string{"absent", "corrupt", "missing-object", "missing-repository"} {
		t.Run(failure, func(t *testing.T) {
			r, source, _, commit := delivery(t)
			branch := "paul/x"
			switch failure {
			case "corrupt":
				if err := os.MkdirAll(filepath.Join(source, ".git", "refs", "heads", "paul"), 0700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(source, ".git", "refs", "heads", branch), "broken\n")
			case "missing-object":
				if err := os.MkdirAll(filepath.Join(source, ".git", "refs", "heads", "paul"), 0700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(source, ".git", "refs", "heads", branch), strings.Repeat("a", 40)+"\n")
			case "missing-repository":
				if err := os.Rename(source, source+"-away"); err != nil {
					t.Fatal(err)
				}
			}
			_, exists, err := r.readBranch(ctx, branch)
			if exists || (err != nil) != (failure != "absent") {
				t.Fatal("wrong read outcome", exists, err)
			}
			for _, exact := range []bool{false, true} {
				var found bool
				if exact {
					_, found, err = r.DeliveredTo(ctx, commit, branch)
				} else {
					_, found, err = r.Delivered(ctx, commit, branch)
				}
				if found || (err != nil) != (failure != "absent") {
					t.Fatal("wrong reconciliation", exact, found, err)
				}
			}
		})
	}
}

func TestDeliveryFindsNumberedBranchAcrossDeletedNames(t *testing.T) {
	r, source, _, commit := delivery(t)
	git(t, source, "branch", "paul/x", "main")
	git(t, source, "branch", "paul/x-2", "main")
	branch, err := r.Deliver(ctx, commit, "paul/x")
	if err != nil || branch != "paul/x-3" {
		t.Fatal(branch, err)
	}
	git(t, source, "branch", "-D", "paul/x", "paul/x-2")
	for _, exact := range []bool{true, false} {
		var name string
		var found bool
		if exact {
			name, found, err = r.DeliveredTo(ctx, commit, branch)
		} else {
			name, found, err = r.Delivered(ctx, commit, "paul/x")
		}
		if err != nil || !found || name != branch {
			t.Fatal(exact, name, found, err)
		}
	}
}

func TestDeliveryNeverMovesTakenDestination(t *testing.T) {
	r, source, base, commit := delivery(t)
	branch, err := r.Destination(ctx, commit, "paul/x")
	if err != nil {
		t.Fatal(err)
	}
	git(t, source, "branch", branch, "main")
	if _, err := r.DeliverTo(ctx, commit, branch); !errors.Is(err, ErrBranchTaken) {
		t.Fatal(err)
	}
	if got := git(t, source, "rev-parse", branch); got != base {
		t.Fatal("moved owner's branch", got)
	}
	if _, found, err := r.DeliveredTo(ctx, commit, branch); err == nil || found {
		t.Fatal("unexpected tip treated as absent", found, err)
	}
}

func TestDestinationRejectsUnreadableHEADButAllowsDetachedHEAD(t *testing.T) {
	r, source, base, commit := delivery(t)
	git(t, source, "checkout", "--detach", "-q", base)
	if _, err := r.Destination(ctx, commit, "paul/x"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(source, ".git", "HEAD"), "broken\n")
	if _, err := r.Destination(ctx, commit, "paul/x"); err == nil {
		t.Fatal("ignored symbolic-ref failure")
	}
}

func TestDeliverySourceSubdirectoryStillOpens(t *testing.T) {
	source := ownerRepo(t)
	sub := filepath.Join(source, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := Open(ctx, t.TempDir(), sub, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	commit := git(t, source, "rev-parse", "main")
	branch, err := r.Destination(ctx, commit, "crew/sub")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DeliverTo(ctx, commit, branch); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.DeliveredTo(ctx, commit, branch); err != nil || !found {
		t.Fatal(found, err)
	}
	// Ordinary commands still discover the containing repository.
	if _, err := run(ctx, sub, "rev-parse", "--git-dir"); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryNeverDiscoversParentAfterMetadataDisappears(t *testing.T) {
	parent := ownerRepo(t)
	source := filepath.Join(parent, "nested")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, source, "init", "-q", "-b", "main")
	write(t, filepath.Join(source, "file"), "nested")
	git(t, source, "add", "file")
	git(t, source, "commit", "-q", "-m", "nested")
	project := t.TempDir()
	r, err := Open(ctx, project, source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	commit := git(t, source, "rev-parse", "main")
	if err := os.Rename(filepath.Join(source, ".git"), filepath.Join(source, "metadata-away")); err != nil {
		t.Fatal(err)
	}
	for _, exact := range []bool{false, true} {
		var found bool
		if exact {
			_, found, err = r.DeliveredTo(ctx, commit, "crew/x")
		} else {
			_, found, err = r.Delivered(ctx, commit, "crew/x")
		}
		if err == nil || found {
			t.Fatal("read parent as delivery repository", exact, found, err)
		}
	}
	if _, err := Open(ctx, project, source, nil, SignAsOwner); err == nil || !strings.Contains(err.Error(), "crew-assistant.deliveryRepository") || !strings.Contains(err.Error(), "config --unset") {
		t.Fatal("restart must reject enclosing repository and explain how to reset the pin", err)
	}
	write(t, filepath.Join(source, ".git"), "gitdir: metadata-away\n")
	git(t, r.Workspace(), "config", "--unset", "crew-assistant.deliveryRepository")
	reopened, err := Open(ctx, project, source, nil, SignAsOwner)
	if err != nil {
		t.Fatal("resetting the pin did not recover the relocated repository", err)
	}
	if got := git(t, reopened.sourceGitDir, "rev-parse", "main"); got != commit {
		t.Fatal("reset discovered the wrong repository", got, commit)
	}
}

func TestDeliveryRepositoryIdentityFollowsConfiguredSource(t *testing.T) {
	source := ownerRepo(t)
	project := t.TempDir()
	r, err := Open(ctx, project, source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	commit := git(t, source, "rev-parse", "main")
	git(t, source, "branch", "crew/old", commit)
	replacement := ownerRepo(t)
	r, err = Open(ctx, project, replacement, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.DeliveredTo(ctx, commit, "crew/old"); err != nil || found {
		t.Fatal("read former repository after source changed", found, err)
	}
}

func TestLegacyDeliveryCloneNeverDiscoversDamagedSourcesParent(t *testing.T) {
	parent := ownerRepo(t)
	source := filepath.Join(parent, "legacy")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, source, "init", "-q", "-b", "main")
	write(t, filepath.Join(source, "file"), "legacy")
	git(t, source, "add", "file")
	git(t, source, "commit", "-q", "-m", "legacy")
	project := t.TempDir()
	r, err := Open(ctx, project, source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	git(t, r.Workspace(), "config", "--unset", "crew-assistant.deliveryRepository")
	if err := os.Rename(filepath.Join(source, ".git"), filepath.Join(source, "metadata-away")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, project, source, nil, SignAsOwner); err == nil {
		t.Fatal("legacy restart discovered enclosing repository")
	}
}

func TestReopeningPinnedDeliveryRepositoryDoesNotRewriteConfig(t *testing.T) {
	source := ownerRepo(t)
	project := t.TempDir()
	r, err := Open(ctx, project, source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.Workspace(), ".git", "config.lock"), "")
	if _, err := Open(ctx, project, source, nil, SignAsOwner); err != nil {
		t.Fatal("reopening contended for an unchanged configuration", err)
	}
}

func TestRefCreationRefusalIsNotATakenBranch(t *testing.T) {
	for _, cause := range []string{"parent-ref", "child-ref", "stale-lock"} {
		t.Run(cause, func(t *testing.T) {
			r, source, _, commit := delivery(t)
			switch cause {
			case "parent-ref":
				git(t, source, "branch", "paul", "main")
			case "child-ref":
				git(t, source, "branch", "paul/x/child", "main")
			case "stale-lock":
				write(t, filepath.Join(source, ".git", "refs", "heads", "paul", "x.lock"), "")
			}
			branch, err := r.Destination(ctx, commit, "paul/x")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.DeliverTo(ctx, commit, branch); !errors.Is(err, ErrNotDelivered) || errors.Is(err, ErrBranchTaken) {
				t.Fatal("refusal misclassified as race", err)
			}
			if _, exists, err := r.readBranch(ctx, branch); err != nil || exists {
				t.Fatal(exists, err)
			}
		})
	}
}

func TestConcurrentOpenPinsLegacyCloneOnce(t *testing.T) {
	source := ownerRepo(t)
	project := t.TempDir()
	r, err := Open(ctx, project, source, nil, SignAsOwner)
	if err != nil {
		t.Fatal(err)
	}
	git(t, r.Workspace(), "config", "--unset", "crew-assistant.deliveryRepository")
	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := Open(ctx, project, source, nil, SignAsOwner)
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenLocksOnlySerializeTheSameProjectAndAreReleased(t *testing.T) {
	project, other := t.TempDir(), t.TempDir()
	unlock := lockOpen(project)
	// An unrelated project must proceed even while this project's lock is held.
	unlocked := make(chan struct{})
	go func() { release := lockOpen(other); release(); close(unlocked) }()
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		unlock()
		t.Fatal("unrelated project blocked")
	}
	unlock()
	openLocks.Lock()
	defer openLocks.Unlock()
	if len(openLocks.paths) != 0 {
		t.Fatal("finished opens retained locks", len(openLocks.paths))
	}
}

func TestOnlyLegacyIntentsSearchBeyondUnusedNames(t *testing.T) {
	r, _, _, commit := delivery(t)
	if _, err := r.DeliverTo(ctx, commit, "paul/x-100"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.DeliveredBeforeIntent(ctx, commit, "paul/x"); err != nil || found {
		t.Fatal("fresh delivery searched beyond its first free destination", found, err)
	}
	if name, found, err := r.Delivered(ctx, commit, "paul/x"); err != nil || !found || name != "paul/x-100" {
		t.Fatal("legacy intent missed its last supported destination", name, found, err)
	}
}
