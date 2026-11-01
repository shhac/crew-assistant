package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/spf13/cobra"
)

func registerTaskChecks(cmd *cobra.Command, o *options) {
	var project string
	list := &cobra.Command{Use: "checks [<task>]", Short: "List after-landing owner checks", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			if cmd.Flags().Changed("project") {
				return errors.New("--project cannot be combined with a task; omit the task to filter the backlog")
			}
			t, err := o.findTask(args[0])
			if err != nil {
				return err
			}
			return o.emit(t.Checklist)
		}
		v, err := o.snapshot()
		if err != nil {
			return err
		}
		out := []core.OutstandingCheck{}
		for _, c := range v.AfterLanding {
			if project == "" || strings.HasPrefix(c.ProjectID, project) || strings.EqualFold(c.Project, project) {
				out = append(out, c)
			}
		}
		return o.emit(out)
	}}
	list.Flags().StringVar(&project, "project", "", "Only this project, by id or title")
	var done, skip, undo bool
	var note string
	check := &cobra.Command{Use: "check <task> <step-number|exact text>", Short: "Mark an after-landing owner check", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		n := 0
		for _, set := range []bool{done, skip, undo} {
			if set {
				n++
			}
		}
		if n != 1 {
			return errors.New("choose exactly one of --done, --skip or --undo")
		}
		if skip && strings.TrimSpace(note) == "" {
			return errors.New("--skip requires --note")
		}
		t, err := o.findTask(args[0])
		if err != nil {
			return err
		}
		step := args[1]
		if number, err := strconv.Atoi(step); err == nil {
			if number < 1 || number > len(t.Checklist) {
				return errors.New("step number is outside the checklist")
			}
			step = t.Checklist[number-1].Step
		}
		status := "outstanding"
		if done {
			status = "done"
		}
		if skip {
			status = "skipped"
		}
		var out core.Task
		if err := o.requestInto("POST", taskPath(t, "owner-checks"), map[string]string{"step": step, "status": status, "note": note}, &out); err != nil {
			return err
		}
		return o.emit(out)
	}}
	check.Flags().BoolVar(&done, "done", false, "Mark done")
	check.Flags().BoolVar(&skip, "skip", false, "Skip with a note")
	check.Flags().BoolVar(&undo, "undo", false, "Return to outstanding")
	check.Flags().StringVar(&note, "note", "", "Why this check is skipped")
	cmd.AddCommand(list, check)
}
