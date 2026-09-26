package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/work"
)

// registerTask is looking at a task's work, and changing it by hand: take
// the latest draft into your own repository, change it, and hand it back
// as the next draft.
func registerTask(root *cobra.Command, o *options) {
	cmd := &cobra.Command{Use: "task", Short: "Look at a task's work, or change its draft by hand"}

	var project string
	var all bool
	list := &cobra.Command{Use: "list", Short: "List tasks: unfinished ones unless --all", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		snap, err := o.snapshot()
		if err != nil {
			return err
		}
		titles := map[string]string{}
		for _, p := range snap.Projects {
			titles[p.ID] = p.Title
		}
		for _, t := range snap.Tasks {
			if (!all && t.Finished()) || (project != "" && !strings.HasPrefix(t.ProjectID, project) && !strings.EqualFold(titles[t.ProjectID], project)) {
				continue
			}
			if err := o.emit(map[string]string{"id": t.ID, "project": titles[t.ProjectID], "status": t.Status, "stage": t.Stage, "objective": t.Objective}); err != nil {
				return err
			}
		}
		return nil
	}}
	list.Flags().StringVar(&project, "project", "", "Only this project, by id or title")
	list.Flags().BoolVar(&all, "all", false, "Include finished tasks")

	show := &cobra.Command{Use: "show <task>", Short: "Show where a task's work is: its branch, latest draft and workspace", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		place, t, err := o.place(args[0])
		if err != nil {
			return err
		}
		return o.emit(struct {
			Objective string `json:"objective"`
			work.TaskPlace
		}{t.Objective, place})
	}}

	path := &cobra.Command{Use: "path <task>", Short: "Print the daemon's workspace for a task; each step resets it, so look but don't change", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		place, _, err := o.place(args[0])
		if err != nil {
			return err
		}
		return o.emit(map[string]string{"path": place.Workspace})
	}}

	var worktree, branch string
	var force bool
	checkout := &cobra.Command{Use: "checkout <task>", Short: "Put a task's latest draft in your repository as a branch, or a worktree, to change it by hand", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		place, _, err := o.place(args[0])
		if err != nil {
			return err
		}
		if place.Repo == "" || place.Draft == nil || place.Draft.Ref == "" {
			return errors.New("only a code task with a draft can be checked out")
		}
		if branch == "" {
			branch = place.Branch
		}
		ctx := cmd.Context()
		if err := gitrepo.CheckoutDraft(ctx, place.Repo, place.Workspace, place.Draft.Ref, branch, force); err != nil {
			return err
		}
		out := map[string]any{"repo": place.Repo, "branch": branch, "commit": place.Draft.Ref, "draft": place.Draft.N}
		if worktree != "" {
			if worktree, err = filepath.Abs(worktree); err != nil {
				return err
			}
			if err := gitrepo.AddWorktree(ctx, place.Repo, worktree, branch); err != nil {
				return err
			}
			out["worktree"] = worktree
		}
		out["next"] = fmt.Sprintf("Commit your change on %s, then run crew-assistant task adopt %s", branch, short(place.TaskID))
		return o.emit(out)
	}}
	checkout.Flags().StringVar(&worktree, "worktree", "", "Also check the branch out in a new worktree at this directory")
	checkout.Flags().StringVar(&branch, "branch", "", "The branch to use (default: the task's own, crew-task/<id>)")
	checkout.Flags().BoolVar(&force, "force", false, "Replace the branch even if it has commits the draft doesn't")

	var note string
	var approve bool
	adopt := &cobra.Command{Use: "adopt <task> [<ref>]", Short: "Take a commit of yours as the task's next draft (default: its branch in your repository)", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		t, err := o.findTask(args[0])
		if err != nil {
			return err
		}
		ref := ""
		if len(args) == 2 {
			ref = args[1]
		}
		var adopted core.Task
		if err := o.requestInto("POST", taskPath(t, "drafts"), map[string]any{"ref": ref, "note": note, "approve": approve}, &adopted); err != nil {
			return err
		}
		draft := adopted.Revisions[len(adopted.Revisions)-1]
		return o.emit(map[string]any{"task": adopted.ID, "draft": draft.N, "commit": draft.Ref, "files": len(draft.Files), "approved": adopted.Approved == draft.N, "status": adopted.Status})
	}}
	adopt.Flags().StringVar(&note, "note", "", "What you changed (default: the commit's subject)")
	adopt.Flags().BoolVar(&approve, "approve", false, "Approve it as you adopt it: reviewers are skipped, QA still runs before it lands")

	cmd.AddCommand(list, show, path, checkout, adopt)
	root.AddCommand(cmd)
}

// findTask is the task an id or a unique start of one names.
func (o *options) findTask(id string) (core.Task, error) {
	snap, err := o.snapshot()
	if err != nil {
		return core.Task{}, err
	}
	id = strings.TrimSpace(id)
	var matches []core.Task
	for _, t := range snap.Tasks {
		if t.ID == id {
			return t, nil
		}
		if id != "" && strings.HasPrefix(t.ID, id) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return core.Task{}, fmt.Errorf("there is no task %q", id)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, t := range matches {
		ids[i] = t.ID
	}
	slices.Sort(ids)
	return core.Task{}, fmt.Errorf("%q could be any of %s; give more of the id", id, strings.Join(ids, ", "))
}

func (o *options) place(id string) (work.TaskPlace, core.Task, error) {
	t, err := o.findTask(id)
	if err != nil {
		return work.TaskPlace{}, core.Task{}, err
	}
	var place work.TaskPlace
	err = o.requestInto("GET", taskPath(t, "place"), nil, &place)
	return place, t, err
}

func (o *options) snapshot() (core.Snapshot, error) {
	var snap core.Snapshot
	err := o.requestInto("GET", "/api/state", nil, &snap)
	return snap, err
}

// requestInto is request with the daemon's answer decoded into out.
func (o *options) requestInto(method, path string, value, out any) error {
	v, err := o.request(method, path, value)
	if err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func taskPath(t core.Task, rest string) string {
	return "/api/projects/" + url.PathEscape(t.ProjectID) + "/tasks/" + url.PathEscape(t.ID) + "/" + rest
}

func short(id string) string { return id[:min(len(id), 8)] }
