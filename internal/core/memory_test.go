package core

import (
	"errors"
	"strings"
	"testing"
)

func TestCorrectingAMemoryKeepsWhatWasBelievedBefore(t *testing.T) {
	s, _ := fixture(t)
	original, err := s.RememberKind(testContext, "worker-model", "Worker model information is unavailable.", "observation", "assistant")
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := s.Correct(testContext, original.ID, "The project worker runs Opus 5.", "observation")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	var before, after *Memory
	for i := range v.Memories {
		switch v.Memories[i].ID {
		case original.ID:
			before = &v.Memories[i]
		case corrected.ID:
			after = &v.Memories[i]
		}
	}
	if before == nil {
		t.Fatal("correcting a memory discarded the original")
	}
	if before.SupersededAt.IsZero() {
		t.Fatal("the original memory is not marked superseded")
	}
	if before.Content != "Worker model information is unavailable." {
		t.Fatal("the original memory was rewritten instead of superseded")
	}
	if after == nil || after.Supersedes != original.ID {
		t.Fatalf("the replacement does not record what it replaced: %+v", after)
	}
	if after.Kind != "observation" || after.Source != "assistant" {
		t.Fatalf("classification was not carried forward: %+v", after)
	}
}

// The owner's memories and each assistant's own are separate stores: the
// same key in each is its own memory, a correction stays in its store, and
// forgetting an assistant takes only its own.
func TestAnAssistantsOwnMemoriesAreKeptApart(t *testing.T) {
	s, _ := fixture(t)
	owner, _ := s.Remember(testContext, "tone", "The owner likes short answers.")
	mine, err := s.RememberAbout(testContext, "iris", "tone", "I open with the outcome.")
	if err != nil || mine.ID == owner.ID || mine.Assistant != "iris" {
		t.Fatalf("%+v %v", mine, err)
	}
	updated, _ := s.RememberAbout(testContext, "iris", "tone", "I open with the outcome, then the evidence.")
	if updated.ID != mine.ID {
		t.Fatal("an assistant's own memory was not updated in place")
	}
	corrected, err := s.Correct(testContext, mine.ID, "I lead with a recommendation.", "")
	if err != nil || corrected.Assistant != "iris" {
		t.Fatalf("%+v %v", corrected, err)
	}
	if _, err := s.RememberAbout(testContext, "", "tone", "Anyone"); err == nil {
		t.Fatal("a memory about no assistant")
	}
	if err := s.ForgetAssistant(testContext, "iris"); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Snapshot(testContext)
	if len(v.Memories) != 1 || v.Memories[0].ID != owner.ID || v.Memories[0].Content != "The owner likes short answers." {
		t.Fatalf("%+v", v.Memories)
	}
}

// Activity is an audit trail the owner reads; a correction names the memory
// rather than restating what it now says.
func TestCorrectionActivityDoesNotRestateContent(t *testing.T) {
	s, _ := fixture(t)
	original, err := s.RememberKind(testContext, "tone", "Keep updates brief.", "preference", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Correct(testContext, original.ID, "Keep updates brief and lead with a recommendation.", ""); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Snapshot(testContext)
	found := false
	for _, entry := range v.Activity {
		if entry.Kind != "memory.corrected" {
			continue
		}
		found = true
		if strings.Contains(entry.Summary, "lead with a recommendation") {
			t.Fatalf("correction activity restated the memory content: %q", entry.Summary)
		}
	}
	if !found {
		t.Fatal("correcting a memory recorded no activity")
	}
}

func TestCorrectingTwiceIsRefusedRatherThanForkingHistory(t *testing.T) {
	s, _ := fixture(t)
	original, err := s.Remember(testContext, "tone", "Keep updates brief.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Correct(testContext, original.ID, "First correction.", ""); err != nil {
		t.Fatal(err)
	}
	_, err = s.Correct(testContext, original.ID, "Second correction.", "")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected a conflict for an already-corrected memory, got %v", err)
	}
}

// Nothing infers a category from the text: an older memory stays uncategorized.
func TestMemoryKindIsNeverInferred(t *testing.T) {
	s, _ := fixture(t)
	m, err := s.Remember(testContext, "legacy", "Something recorded before categories existed.")
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "" {
		t.Fatalf("kind = %q, want it left uncategorized", m.Kind)
	}
	if _, err := s.RememberKind(testContext, "bad", "content", "guess", "owner"); err == nil {
		t.Fatal("an unknown memory kind was accepted")
	}
}

// The assistant's remember tool deduplicates on key. The category the owner
// chose survives that upsert; provenance follows whoever recorded the content,
// so an assistant rewrite is not still attributed to the owner.
func TestRememberKeepsClassificationAndAttributesTheWriter(t *testing.T) {
	s, _ := fixture(t)
	if _, err := s.RememberKind(testContext, "tone", "Keep updates brief.", "preference", "owner"); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Remember(testContext, "tone", "Keep updates brief and direct.")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Kind != "preference" {
		t.Fatalf("an update by key dropped the owner's category: %+v", updated)
	}
	if updated.Source != "assistant" {
		t.Fatalf("source = %q, want the writer of the current content", updated.Source)
	}
}

// The assistant's remember tool records provenance; a blank source would make
// every assistant memory read as "Source not recorded" in the workspace.
func TestRememberRecordsAssistantProvenance(t *testing.T) {
	s, _ := fixture(t)
	m, err := s.Remember(testContext, "worker-model", "The project worker runs Opus 5.")
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != "assistant" {
		t.Fatalf("source = %q, want assistant", m.Source)
	}
}

// A correction keeps the old memory under the same key. An update must land on
// the memory the owner is shown, not on the tombstone behind it — otherwise the
// newest text is filed as already-corrected and the stale text stays live.
func TestRememberAfterCorrectionUpdatesTheLiveMemory(t *testing.T) {
	s, _ := fixture(t)
	original, err := s.RememberKind(testContext, "tone", "Old text.", "preference", "owner")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := s.Correct(testContext, original.ID, "Corrected text.", "")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Remember(testContext, "tone", "Newest text.")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != replacement.ID {
		t.Fatalf("the update landed on %s, want the live memory %s", updated.ID, replacement.ID)
	}
	if !updated.SupersededAt.IsZero() {
		t.Fatal("the live memory was marked as already corrected")
	}
	v, _ := s.Snapshot(testContext)
	before := memoryByID(&v, original.ID)
	if before == nil || before.Content != "Old text." {
		t.Fatalf("the superseded original was rewritten: %+v", before)
	}
	if before.SupersededAt.IsZero() {
		t.Fatal("the superseded original stopped being marked corrected")
	}
}

// Forgetting either end of a correction chain must leave the list honest: no
// replacement pointing at a memory that is gone, and no memory marked
// corrected with nothing replacing it.
func TestForgetRepairsTheCorrectionChain(t *testing.T) {
	s, _ := fixture(t)
	original, err := s.RememberKind(testContext, "tone", "Old text.", "preference", "owner")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := s.Correct(testContext, original.ID, "Corrected text.", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(testContext, original.ID); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Snapshot(testContext)
	live := memoryByID(&v, replacement.ID)
	if live == nil {
		t.Fatal("forgetting the original removed the replacement")
	}
	if live.Supersedes != "" {
		t.Fatalf("the replacement still claims a predecessor that is gone: %q", live.Supersedes)
	}

	s2, _ := fixture(t)
	first, _ := s2.RememberKind(testContext, "tone", "Old text.", "preference", "owner")
	second, _ := s2.Correct(testContext, first.ID, "Corrected text.", "")
	if err := s2.Forget(testContext, second.ID); err != nil {
		t.Fatal(err)
	}
	v2, _ := s2.Snapshot(testContext)
	restored := memoryByID(&v2, first.ID)
	if restored == nil {
		t.Fatal("forgetting the replacement removed the original too")
	}
	if !restored.SupersededAt.IsZero() {
		t.Fatal("a memory is still marked corrected with nothing replacing it")
	}
}
