package cli

import (
	"sort"
	"strings"
	"unicode"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/spf13/cobra"
)

// Completions use only the command schema and selected local configuration.
// They never open the state database, call the daemon, inspect accounts, or start
// model/Docker processes. Cobra provides the shell script generation command.
func registerCompletions(root *cobra.Command, o *options) {
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if !cmd.HasSubCommands() && cmd.ValidArgsFunction == nil {
			cmd.ValidArgsFunction = completeStatic(nil)
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
	values := map[string][]string{}
	for _, k := range configKeys(o) {
		values[k.Name] = k.Values
	}
	set, _, _ := root.Find([]string{"config", "set"})
	keys := set.ValidArgsFunction
	set.ValidArgsFunction = func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		switch {
		case len(args) == 0:
			return keys(cmd, args, prefix)
		case len(args) > 1:
			return completionValues(nil, prefix)
		case strings.HasSuffix(args[0], ".home"):
			return nil, cobra.ShellCompDirectiveFilterDirs
		case args[0] == "model.model":
			return completionValues(configuredModels(o), prefix)
		}
		return completionValues(values[args[0]], prefix)
	}
	serve, _, _ := root.Find([]string{"serve"})
	_ = serve.RegisterFlagCompletionFunc("tailscale", completeStatic(values["dashboard.tailscale"]))
	_ = serve.RegisterFlagCompletionFunc("tailscale-port", completeStatic(values["dashboard.tailscale_port"]))
	_ = serve.RegisterFlagCompletionFunc("http", completeStatic(nil))
	login, _, _ := root.Find([]string{"model", "login"})
	_ = login.RegisterFlagCompletionFunc("engine", completeStatic([]string{"codex", "claude"}))
}

func completeStatic(values []string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return completionValues(values, prefix)
	}
}

func completionValues(values []string, prefix string) ([]string, cobra.ShellCompDirective) {
	seen := make(map[string]bool)
	var matches []string
	for _, value := range values {
		// Configured names are data: control bytes must not become shell protocol
		// directives or tab-separated completion descriptions.
		if value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 || !strings.HasPrefix(value, prefix) || seen[value] {
			continue
		}
		seen[value] = true
		matches = append(matches, value)
	}
	sort.Strings(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func configuredModels(o *options) []string {
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return nil
	}
	return []string{cfg.Model.Model}
}
