package cli

import (
	"reflect"
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
	set, _, _ := root.Find([]string{"config", "set"})
	keys := completionConfigKeys(reflect.TypeFor[config.Config](), "")
	set.ValidArgsFunction = func(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return completionValues(keys, prefix)
		case 1:
			if args[0] == "model.codex_home" || args[0] == "worker_model.codex_home" {
				return nil, cobra.ShellCompDirectiveFilterDirs
			}
			values := completionConfigValues(args[0])
			if args[0] == "model.model" || args[0] == "worker_model.model" {
				values = configuredModels(o)
			}
			return completionValues(values, prefix)
		default:
			return completionValues(nil, prefix)
		}
	}
	serve, _, _ := root.Find([]string{"serve"})
	_ = serve.RegisterFlagCompletionFunc("tailscale", completeStatic(completionConfigValues("dashboard.tailscale")))
	_ = serve.RegisterFlagCompletionFunc("tailscale-port", completeStatic(completionConfigValues("dashboard.tailscale_port")))
	_ = serve.RegisterFlagCompletionFunc("http", completeStatic(nil))
	login, _, _ := root.Find([]string{"model", "login"})
	_ = login.RegisterFlagCompletionFunc("profile", completeStatic([]string{"assistant", "worker"}))
	worker, _, _ := root.Find([]string{"worker", "serve"})
	_ = worker.RegisterFlagCompletionFunc("engine", completeStatic(completionConfigValues("worker_model.engine")))
	_ = worker.RegisterFlagCompletionFunc("effort", completeStatic(completionConfigValues("worker_model.effort")))
	_ = worker.RegisterFlagCompletionFunc("model", func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return completionValues(configuredModels(o), prefix)
	})
	_ = worker.RegisterFlagCompletionFunc("project", func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			return completionValues(nil, prefix)
		}
		var projects []string
		for _, profile := range cfg.Workers {
			projects = append(projects, profile.ProjectID)
		}
		return completionValues(projects, prefix)
	})
	for _, flag := range []string{"image", "http", "token-env"} {
		_ = worker.RegisterFlagCompletionFunc(flag, completeStatic(nil))
	}
	_ = worker.MarkFlagDirname("workspace")
	_ = worker.MarkFlagDirname("worker-state")
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

// Match config set's dotted object traversal. Arrays are set as a whole JSON
// value; suggesting workers.0.id would advertise a path the command rejects.
func completionConfigKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		key := prefix + name
		keys = append(keys, key)
		if field.Type.Kind() == reflect.Struct {
			keys = append(keys, completionConfigKeys(field.Type, key+".")...)
		}
	}
	return keys
}

func completionConfigValues(key string) []string {
	switch key {
	case "assistant.theme":
		return []string{"graphite-sage", "ink-blue", "charcoal-amber"}
	case "assistant.avatar.shape":
		return []string{"orb", "spark", "leaf"}
	case "dashboard.tailscale":
		return []string{"off", "serve"}
	case "dashboard.tailscale_port":
		return []string{"443", "8443", "10000"}
	case "model.engine", "worker_model.engine":
		return []string{"codex", "claude", "openai-compatible"}
	case "model.effort", "worker_model.effort", "chat.loading_phrases.effort":
		return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	}
	return nil
}

func configuredModels(o *options) []string {
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return nil
	}
	return []string{cfg.Model.Model, cfg.WorkerModel.Model}
}
