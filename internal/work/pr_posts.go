package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// ownPost marks what the loop posts on a pull request, so the team never
// takes its own replies for feedback.
const ownPost = "<!-- crew-assistant -->"

// prFooter ends every pull request description the loop writes.
const prFooter = "Opened by crew-assistant for its owner. Its team answers reviews and CI here."

// prText is the pull request's title and description: the implementer's,
// or the request and its latest draft's summary where it wrote none.
func prText(t core.Task) core.PRText {
	var out core.PRText
	if t.Proposal != nil {
		out = core.PRText{Title: t.Proposal.Title, Body: t.Proposal.Body}
	}
	if out.Title == "" {
		out.Title = text.Clip(t.Objective, 120)
	}
	if out.Body == "" && len(t.Revisions) > 0 {
		out.Body = text.Clip(t.Revisions[len(t.Revisions)-1].Summary, 3000)
	}
	return out
}

// described fingerprints the text GitHub was given, so a rewrite is noticed.
func described(pr core.PRText) string {
	sum := sha256.Sum256([]byte(pr.Title + "\x00" + pr.Body))
	return hex.EncodeToString(sum[:8])
}

// description is what GitHub is given as the pull request's description.
func description(pr core.PRText) string {
	return strings.TrimSpace(pr.Body) + "\n\n" + prFooter
}

// maxPostFailures is how often GitHub may refuse a post before the team
// gives it up, so one bad reply never holds the pull request.
const maxPostFailures = 3

// postOutbox posts what the team has to say on the pull request, one at a
// time, each only once the draft it came with is pushed and each taken off
// the outbox in the change after GitHub has it, so a restart never posts
// twice and a failure loses nothing. One GitHub refuses is tried again at
// the next look, and given up after maxPostFailures.
func (lp *Loop) postOutbox(ctx context.Context, taskID, repo string, number int) error {
	lp.posting.Lock()
	defer lp.posting.Unlock()
	for {
		snap, err := lp.Core.Snapshot(ctx)
		if err != nil {
			return err
		}
		t, ok := snap.FindTask(taskID)
		if !ok {
			return nil
		}
		post, ok := nextPost(t)
		if !ok {
			return nil
		}
		sent := lp.sendPost(ctx, repo, number, post)
		retry := false
		if _, err = lp.Core.UpdateTask(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
			if t.Proposal == nil {
				return "", nil
			}
			outbox := t.Proposal.Outbox
			i := slices.Index(outbox, post)
			if i < 0 {
				return "", nil
			}
			if sent != nil {
				outbox[i].Failures++
				if retry = outbox[i].Failures < maxPostFailures; retry {
					return "", nil
				}
			}
			t.Proposal.Outbox = slices.Delete(outbox, i, i+1)
			if sent != nil {
				return fmt.Sprintf("Gave up posting %s's reply on pull request #%d: %s", post.By, number, text.Clip(sent.Error(), 200)), nil
			}
			return "", nil
		}); err != nil {
			return err
		}
		if retry {
			return nil
		}
	}
}

// nextPost is the first post the task's pull request has the draft for.
func nextPost(t core.Task) (core.PRPost, bool) {
	if t.Proposal == nil {
		return core.PRPost{}, false
	}
	pushed := 0
	for _, r := range t.Revisions {
		if r.Ref == t.Proposal.Pushed {
			pushed = r.N
		}
	}
	for _, post := range t.Proposal.Outbox {
		if post.Revision <= pushed {
			return post, true
		}
	}
	return core.PRPost{}, false
}

// sendPost puts one post on the pull request.
func (lp *Loop) sendPost(ctx context.Context, repo string, number int, post core.PRPost) error {
	switch {
	case post.Resolve:
		return lp.github.Resolve(ctx, post.Thread)
	case post.Thread != "":
		return lp.github.Reply(ctx, post.Thread, signed(post))
	}
	return lp.github.Comment(ctx, repo, number, signed(post))
}

// signed is a post as GitHub shows it: whose it is, and marked as the
// team's own so it is never taken for feedback.
func signed(post core.PRPost) string {
	return post.Body + "\n\n— " + post.By + ", for crew-assistant\n" + ownPost
}

// parsePRText reads the implementer's ```pr block: the title and description
// its pull request opens with, or is rewritten to. A problem with it is told
// to the implementer in its next round, as a wake block's is.
func parsePRText(block string) (*core.PRText, string) {
	if block == "" {
		return nil, ""
	}
	var in core.PRText
	if err := json.Unmarshal([]byte(block), &in); err != nil {
		return nil, "the pr block was not valid JSON: " + err.Error()
	}
	in.Title = strings.Join(strings.Fields(in.Title), " ")
	in.Body = strings.TrimSpace(in.Body)
	if in.Title == "" || in.Body == "" {
		return nil, `the pr block needs a "title" and a "body"`
	}
	in.Title, in.Body = text.Clip(in.Title, 120), text.Clip(in.Body, 6000)
	return &in, ""
}

// prHandTo is the teammate an implementer hands a pull request's question
// to, and what it asks them.
type prHandTo struct {
	To       string `json:"to"`
	Question string `json:"question"`
}

// parsePRReply reads the implementer's ```pr-reply block: replies on the
// pull request, in a thread or its conversation, threads its pushed draft
// resolves, and a teammate to hand a question to. Replies wait to be posted
// until the draft they came with is pushed.
func parsePRReply(block, by string, open bool, revision int) ([]core.PRPost, *prHandTo, []string) {
	if block == "" {
		return nil, nil, nil
	}
	if !open {
		return nil, nil, []string{"the pr-reply block was for a pull request that isn't open"}
	}
	var in struct {
		Replies []struct {
			Thread string `json:"thread"`
			Body   string `json:"body"`
		} `json:"replies"`
		Resolve []string  `json:"resolve"`
		HandTo  *prHandTo `json:"hand_to"`
	}
	if err := json.Unmarshal([]byte(block), &in); err != nil {
		return nil, nil, []string{"the pr-reply block was not valid JSON: " + err.Error()}
	}
	var posts []core.PRPost
	var problems []string
	for _, r := range in.Replies {
		body := strings.TrimSpace(r.Body)
		if body == "" {
			problems = append(problems, "a pr-reply reply had no body")
			continue
		}
		posts = append(posts, core.PRPost{Thread: strings.TrimSpace(r.Thread), Body: text.Clip(body, 4000), By: by, Revision: revision})
	}
	for _, thread := range in.Resolve {
		if thread = strings.TrimSpace(thread); thread != "" {
			posts = append(posts, core.PRPost{Thread: thread, Resolve: true, By: by, Revision: revision})
		}
	}
	if h := in.HandTo; h != nil && (strings.TrimSpace(h.To) == "" || strings.TrimSpace(h.Question) == "") {
		problems = append(problems, `a pr-reply hand_to needs "to" and "question"`)
		in.HandTo = nil
	}
	return posts, in.HandTo, problems
}

// prBlockGuide tells the implementer of a change going out as a pull
// request how to give its title and description, and, once it is open, how
// to answer on it: the formats parsePRText and parsePRReply read.
func prBlockGuide(t core.Task) string {
	var b strings.Builder
	if proposed(t) {
		b.WriteString("\nIts pull request is open. Feedback on it reaches you as findings; a review thread names its id. Answer on the pull request by ending your reply with a pr-reply block: replies go in a thread or, without one, in its conversation, and are posted once your draft is pushed; resolve a thread only when a draft you made fixes it. You can hand a question to a teammate, a reviewer, QA or the PM, whose answer is posted there too; the PM can also ask the owner. Feedback that needs nothing from you needs no block.\n```pr-reply\n{\"replies\": [{\"thread\": \"thread id, or empty for the conversation\", \"body\": \"...\"}], \"resolve\": [\"thread id\"], \"hand_to\": {\"to\": \"QA\", \"question\": \"...\"}}\n```\n")
	}
	b.WriteString("\nThis change goes out as a GitHub pull request. End your reply with a pr block giving its title, under 72 characters, and its description, for a reviewer: what changed, why, and how it was checked. Give it again whenever a draft changes what it should say; an open pull request is updated to match.\n```pr\n{\"title\": \"...\", \"body\": \"...\"}\n```\n")
	return b.String()
}
