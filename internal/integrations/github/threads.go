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

// ThreadComment is one comment in a review thread.
type ThreadComment struct {
	Author      Author
	Association string
	Body        string
	CreatedAt   time.Time
}

// threadsQuery reads up to 100 review threads and 50 comments in each: a
// pull request with more is beyond what the team answers in one look.
const threadsQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          comments(first: 50) {
            nodes { author { login } authorAssociation body createdAt }
          }
        }
      }
    }
  }
}`

func (c Client) threads(ctx context.Context, repo string, number int) ([]Thread, error) {
	owner, name, _ := strings.Cut(repo, "/")
	out, err := c.Run(ctx, "api", "graphql", "-f", "query="+threadsQuery, "-f", "owner="+owner, "-f", "name="+name, "-F", "number="+strconv.Itoa(number))
	if err != nil {
		return nil, err
	}
	var answer struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
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
	var threads []Thread
	for _, n := range answer.Data.Repository.PullRequest.ReviewThreads.Nodes {
		th := Thread{ID: n.ID, Resolved: n.IsResolved, Outdated: n.IsOutdated, Path: n.Path, Line: n.Line}
		for _, c := range n.Comments.Nodes {
			th.Comments = append(th.Comments, ThreadComment{Author: c.Author, Association: c.AuthorAssociation, Body: c.Body, CreatedAt: c.CreatedAt})
		}
		threads = append(threads, th)
	}
	return threads, nil
}

func threadID(id string) bool {
	return id != "" && len(id) <= 200 && !strings.ContainsAny(id, " \t\n\"\\")
}

// Comment adds a comment to the pull request's conversation.
func (c Client) Comment(ctx context.Context, repo string, number int, body string) error {
	if !repoName.MatchString(repo) {
		return errors.New("not a GitHub repository name")
	}
	_, err := c.Run(ctx, "pr", "comment", strconv.Itoa(number), "--repo", repo, "--body", body)
	return err
}

// Reply answers a review thread.
func (c Client) Reply(ctx context.Context, thread, body string) error {
	if !threadID(thread) {
		return errors.New("not a review thread")
	}
	_, err := c.Run(ctx, "api", "graphql", "-f", `query=mutation($thread: ID!, $body: String!) { addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $thread, body: $body}) { comment { id } } }`, "-f", "thread="+thread, "-f", "body="+body)
	return err
}

// Resolve marks a review thread resolved.
func (c Client) Resolve(ctx context.Context, thread string) error {
	if !threadID(thread) {
		return errors.New("not a review thread")
	}
	_, err := c.Run(ctx, "api", "graphql", "-f", `query=mutation($thread: ID!) { resolveReviewThread(input: {threadId: $thread}) { thread { id } } }`, "-f", "thread="+thread)
	return err
}

// Edit sets the pull request's title and description.
func (c Client) Edit(ctx context.Context, repo string, number int, title, body string) error {
	if !repoName.MatchString(repo) {
		return errors.New("not a GitHub repository name")
	}
	_, err := c.Run(ctx, "pr", "edit", strconv.Itoa(number), "--repo", repo, "--title", title, "--body", body)
	return err
}

// Close closes the pull request without merging it, saying why.
func (c Client) Close(ctx context.Context, repo string, number int, comment string) error {
	if !repoName.MatchString(repo) {
		return errors.New("not a GitHub repository name")
	}
	_, err := c.Run(ctx, "pr", "close", strconv.Itoa(number), "--repo", repo, "--comment", comment)
	return err
}
