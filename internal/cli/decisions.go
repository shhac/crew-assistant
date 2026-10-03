package cli

import (
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func decisionsCommand(o *options) *cobra.Command {
	parent := &cobra.Command{Use: "decisions", Short: "Export answered decisions"}
	var outputPath string
	export := &cobra.Command{Use: "export", Short: "Export the decision evaluation record as JSONL", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return o.requestBody(http.MethodGet, "/api/decisions/evaluations.jsonl", nil, func(r io.Reader) error {
			w := cmd.OutOrStdout()
			if outputPath != "" {
				f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
				if err != nil {
					return err
				}
				_, err = io.Copy(f, r)
				closeErr := f.Close()
				if err != nil {
					return err
				}
				return closeErr
			}
			_, err := io.Copy(w, r)
			return err
		})
	}}
	export.Flags().StringVar(&outputPath, "output", "", "Write JSONL to a file instead of stdout")
	parent.AddCommand(export)
	return parent
}
