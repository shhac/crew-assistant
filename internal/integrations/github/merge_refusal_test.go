package github

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestMergeRefusalLeavesTransportFailuresUncertain(t *testing.T) {
	for _, tc := range []struct {
		message string
		refused bool
	}{
		{"GraphQL: Base branch policy prohibits the merge (mergePullRequest)", true},
		{"GraphQL: Squash merges are not allowed on this repository", true},
		{"GraphQL: Pull request is not mergeable", true},
		{"GraphQL: Head branch was modified", true},
		{"GraphQL: Merge method unsupported (mergePullRequest)", true},
		{"gh pr: GraphQL: Merge method unsupported (mergePullRequest): exit status 1", true},
		{"GraphQL: unexpected EOF", false},
		{"GraphQL: Resource not accessible by integration (mergePullRequest)", true},
		{"HTTP 403: Forbidden", true},
		{"unknown flag: --squash", true},
		{"response lost", false},
		{"unexpected EOF", false},
		{"context deadline exceeded", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			cause := errors.New(tc.message)
			client := Client{Run: func(context.Context, ...string) ([]byte, error) { return nil, cause }}
			err := client.Merge(context.Background(), "owner/repo", 7, "squash", "checked-head")
			var refusal *MergeRefusal
			if errors.As(err, &refusal) != tc.refused || !errors.Is(err, cause) {
				t.Fatal("wrong merge certainty", err)
			}
		})
	}
}

func TestMergeLocalValidationIsRefusal(t *testing.T) {
	client := Client{Run: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("invalid invocation reached gh")
		return nil, nil
	}}
	for _, input := range []struct{ repo, method string }{{"", "squash"}, {"owner/repo", "unsupported"}} {
		err := client.Merge(context.Background(), input.repo, 7, input.method, "head")
		var refused *MergeRefusal
		if !errors.As(err, &refused) {
			t.Fatal("local validation was uncertain", err)
		}
	}
}

func TestMergeInvocationFailuresAreRefusals(t *testing.T) {
	for _, err := range []error{&exec.Error{Name: "gh", Err: exec.ErrNotFound}, &os.PathError{Op: "fork/exec", Path: "gh", Err: os.ErrPermission}} {
		if !MergeRefused(err) {
			t.Fatal(err)
		}
	}
}
