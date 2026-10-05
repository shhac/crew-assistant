package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Thread is a review thread on the pull request's code.
type Thread struct {
	ID       string
	Resolved bool
	// Outdated is a thread on lines a later push changed.
	Outdated bool
	Path     string
	Line     int
	Comments []ThreadComment
}

// Opener is the thread's first comment, which says whose thread it is.
func (th Thread) Opener() ThreadComment {
	if len(th.Comments) == 0 {
		return ThreadComment{}
	}
	return th.Comments[0]
}

// ThreadComment is one comment in a review thread.
type ThreadComment struct {
	Author      Author
	Association string
	Body        string
	CreatedAt   time.Time
	URL         string
}

// threadsQuery reads merge enrollment plus up to 100 threads and 50 comments in each: a
// pull request with more is beyond what the team answers in one look.
const threadsQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      state
      headRefOid
      mergeCommit { oid }
      autoMergeRequest { enabledAt }
      mergeQueueEntry { id }
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          comments(first: 50) {
            nodes { author { login } authorAssociation body createdAt url }
          }
        }
      }
    }
  }
}`

func (c Client) threads(ctx context.Context, repo string, number int, pr *PR) ([]Thread, error) {
	owner, name, _ := strings.Cut(repo, "/")
	out, err := c.Run(ctx, "api", "graphql", "-f", "query="+threadsQuery, "-f", "owner="+owner, "-f", "name="+name, "-F", "number="+strconv.Itoa(number))
	if err != nil {
		return nil, err
	}
	var answer struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					State            string           `json:"state"`
					HeadRefOid       string           `json:"headRefOid"`
					MergeCommit      json.RawMessage  `json:"mergeCommit"`
					AutoMergeRequest *json.RawMessage `json:"autoMergeRequest"`
					MergeQueueEntry  *json.RawMessage `json:"mergeQueueEntry"`
					ReviewThreads    struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							IsOutdated bool   `json:"isOutdated"`
							Path       string `json:"path"`
							Line       int    `json:"line"`
							Comments   struct {
								Nodes []struct {
									Author            Author    `json:"author"`
									AuthorAssociation string    `json:"authorAssociation"`
									Body              string    `json:"body"`
									CreatedAt         time.Time `json:"createdAt"`
									URL               string    `json:"url"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err = json.Unmarshal(out, &answer); err != nil {
		return nil, fmt.Errorf("gh gave unreadable review threads: %w", err)
	}
	snapshot := answer.Data.Repository.PullRequest
	if snapshot.State == "" || snapshot.HeadRefOid == "" {
		return nil, errors.New("gh gave an incomplete pull request delivery observation")
	}
	// State, head, merge commit and enrollment must describe the same read.
	pr.State, pr.HeadRefOid = snapshot.State, snapshot.HeadRefOid
	pr.MergeCommit = nil
	if len(snapshot.MergeCommit) > 0 {
		if err := json.Unmarshal(snapshot.MergeCommit, &pr.MergeCommit); err != nil {
			return nil, err
		}
	}
	pr.MergeInFlight = snapshot.AutoMergeRequest != nil || snapshot.MergeQueueEntry != nil
	var threads []Thread
	for _, n := range answer.Data.Repository.PullRequest.ReviewThreads.Nodes {
		th := Thread{ID: n.ID, Resolved: n.IsResolved, Outdated: n.IsOutdated, Path: n.Path, Line: n.Line}
		for _, c := range n.Comments.Nodes {
			th.Comments = append(th.Comments, ThreadComment{Author: c.Author, Association: c.AuthorAssociation, Body: c.Body, CreatedAt: c.CreatedAt, URL: c.URL})
		}
		threads = append(threads, th)
	}
	return threads, nil
}

// onThread runs a GraphQL mutation on a review thread, after checking the
// thread's id, with the mutation's other fields after.
func (c Client) onThread(ctx context.Context, thread, query string, fields ...string) error {
	if thread == "" || len(thread) > 200 || strings.ContainsAny(thread, " \t\n\"\\") {
		return errors.New("not a review thread")
	}
	_, err := c.Run(ctx, append([]string{"api", "graphql", "-f", "query=" + query, "-f", "thread=" + thread}, fields...)...)
	return err
}

// Comment adds a comment to the pull request's conversation.
func (c Client) Comment(ctx context.Context, repo string, number int, body string) error {
	_, err := c.pr(ctx, "comment", repo, number, "--body", body)
	return err
}

// Reply answers a review thread.
func (c Client) Reply(ctx context.Context, thread, body string) error {
	return c.onThread(ctx, thread, `mutation($thread: ID!, $body: String!) { addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $thread, body: $body}) { comment { id } } }`, "-f", "body="+body)
}

// Resolve marks a review thread resolved.
func (c Client) Resolve(ctx context.Context, thread string) error {
	return c.onThread(ctx, thread, `mutation($thread: ID!) { resolveReviewThread(input: {threadId: $thread}) { thread { id } } }`)
}

// Edit sets the pull request's title and description.
func (c Client) Edit(ctx context.Context, repo string, number int, title, body string) error {
	_, err := c.pr(ctx, "edit", repo, number, "--title", title, "--body", body)
	return err
}

// Close closes the pull request without merging it, saying why.
func (c Client) Close(ctx context.Context, repo string, number int, comment string) error {
	_, err := c.pr(ctx, "close", repo, number, "--comment", comment)
	return err
}
