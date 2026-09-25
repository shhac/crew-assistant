package cli

import (
	"strconv"

	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/crew-assistant/internal/config"
)

// configCommand is `config get/set/unset/list`. Which file they work on is
// only known once --config is parsed, after the commands exist, so each verb
// runs on a command built for it then.
func configCommand(o *options) *cobra.Command {
	build := func() *cobra.Command {
		return libcli.ConfigCommand(o.globals, configKeys(o), libcli.WithDocument(config.Document(o.configPath), config.Config{}))
	}
	cmd := build()
	for _, verb := range cmd.Commands() {
		verb.RunE = func(c *cobra.Command, args []string) error {
			built, _, err := build().Find([]string{verb.Name()})
			if err != nil {
				return err
			}
			return built.RunE(c, args)
		}
	}
	return cmd
}

// configKeys are the settings `config get/set/unset` reach, each named by its
// path in the file. A change is checked against the whole config before it is
// saved, and goes through the daemon when one is running.
func configKeys(o *options) []libcli.ConfigKey {
	doc := config.Document(o.configPath)
	b := libcli.ConfigBinding[config.Config]{
		Read: func() (config.Config, error) { return config.Load(o.configPath) },
		Update: func(change func(*config.Config) error) error {
			c, err := config.Load(o.configPath)
			if err != nil {
				return err
			}
			if err := change(&c); err != nil {
				return err
			}
			if err := c.Validate(); err != nil {
				return output.Wrap(err, output.FixableByAgent)
			}
			return o.saveConfig(c)
		},
		Default: config.Default,
		Doc:     &doc,
	}
	keys := []libcli.ConfigKey{
		libcli.OneOfKey(b, "model.engine", "The engine the assistant runs on", func(c *config.Config) *string { return &c.Model.Engine }, config.EngineNames),
		libcli.StringKey(b, "model.model", "The assistant's model", func(c *config.Config) *string { return &c.Model.Model }, nil),
		libcli.OneOfKey(b, "model.effort", "The assistant's reasoning effort; empty is the model's default", func(c *config.Config) *string { return &c.Model.Effort }, config.Efforts),
		libcli.IntKey(b, "model.max_tokens", "The most tokens one assistant reply may use", func(c *config.Config) *int { return &c.Model.MaxTokens }, 128, 131072),
		libcli.StringKey(b, "engines.openai-compatible.base_url", "The OpenAI-compatible endpoint", func(c *config.Config) *string { return &c.Engines.OpenAICompatible.BaseURL }, nil),
		libcli.EnvNameKey(b, "engines.openai-compatible.api_key_env", "The environment variable holding the endpoint's key; empty sends none", func(c *config.Config) *string { return &c.Engines.OpenAICompatible.APIKeyEnv }),
		libcli.IntKey(b, "limits.max_model_calls_per_day", "The most assistant model calls a day", func(c *config.Config) *int { return &c.Limits.MaxModelCallsPerDay }, 1, 100000),
		libcli.IntKey(b, "limits.max_model_turns", "The most tool turns in one assistant reply", func(c *config.Config) *int { return &c.Limits.MaxModelTurns }, 1, 32),
		boolKey(b, "chat.loading_phrases.enabled", "Show small-model captions while the assistant thinks", func(c *config.Config) *bool { return &c.Chat.LoadingPhrases.Enabled }),
		libcli.StringKey(b, "assistant.name", "The assistant's name", func(c *config.Config) *string { return &c.Assistant.Name }, nil),
		libcli.StringKey(b, "assistant.personality", "How the assistant speaks", func(c *config.Config) *string { return &c.Assistant.Personality }, nil),
		libcli.OneOfKey(b, "assistant.theme", "The dashboard's appearance", func(c *config.Config) *string { return &c.Assistant.Theme }, []string{config.ThemeSystem, config.ThemeLight, config.ThemeDark}),
		libcli.JSONKey[config.Config, config.Avatar](b, "assistant.avatar", "The assistant's avatar, as JSON", func(c *config.Config) *config.Avatar { return &c.Assistant.Avatar }, nil),
		libcli.StringKey(b, "dashboard.addr", "The loopback address the dashboard listens on", func(c *config.Config) *string { return &c.Dashboard.Addr }, nil),
		libcli.OneOfKey(b, "dashboard.tailscale", "Share the dashboard on the tailnet with Tailscale Serve", func(c *config.Config) *string { return &c.Dashboard.Tailscale }, []string{"off", "serve"}),
		withValues(libcli.IntKey(b, "dashboard.tailscale_port", "The port Tailscale Serve uses", func(c *config.Config) *int { return &c.Dashboard.TailscalePort }, 1, 65535), "443", "8443", "10000"),
		libcli.JSONKey[config.Config, []string](b, "dashboard.allowed_users", "The tailnet logins allowed in, as a JSON array", func(c *config.Config) *[]string { return &c.Dashboard.AllowedUsers }, nil),
		libcli.EnvNameKey(b, "slack.bot_token_env", "The environment variable holding the Slack bot token", func(c *config.Config) *string { return &c.Slack.BotTokenEnv }),
		libcli.EnvNameKey(b, "slack.app_token_env", "The environment variable holding the Slack app token", func(c *config.Config) *string { return &c.Slack.AppTokenEnv }),
		libcli.StringKey(b, "slack.owner_user_id", "The owner's Slack user ID", func(c *config.Config) *string { return &c.Slack.OwnerUserID }, nil),
		boolKey(b, "linear.import_assignments", "Import Linear issues assigned to the owner", func(c *config.Config) *bool { return &c.Linear.ImportAssignments }),
		libcli.EnvNameKey(b, "linear.api_key_env", "The environment variable holding the Linear API key", func(c *config.Config) *string { return &c.Linear.APIKeyEnv }),
		libcli.JSONKey[config.Config, []string](b, "linear.team_ids", "The Linear teams to import from, as a JSON array", func(c *config.Config) *[]string { return &c.Linear.TeamIDs }, nil),
		libcli.JSONKey[config.Config, []config.Connection](b, "connections", "The agent CLIs the assistant may query, as a JSON array", func(c *config.Config) *[]config.Connection { return &c.Connections }, nil),
	}
	for _, name := range []string{"codex", "claude"} {
		engine := func(c *config.Config) *config.CLIEngine {
			if name == "codex" {
				return &c.Engines.Codex
			}
			return &c.Engines.Claude
		}
		prefix := "engines." + name + "."
		keys = append(keys,
			libcli.StringKey(b, prefix+"bin", "The "+name+" executable; empty is "+name+" on PATH", func(c *config.Config) *string { return &engine(c).Bin }, nil),
			libcli.PathKey(b, prefix+"home", "The "+name+" login home; empty is the default", func(c *config.Config) *string { return &engine(c).Home }),
			libcli.OptionalIntKey(b, prefix+"usage_floor.5h_percent", "The share of the 5-hour window team roles leave unused; 0 turns it off", func(c *config.Config) **int { return &engine(c).UsageFloor.FiveHourPercent }, 0, 100),
			libcli.OptionalIntKey(b, prefix+"usage_floor.1w_percent", "The share of the weekly window team roles leave unused; 0 turns it off", func(c *config.Config) **int { return &engine(c).UsageFloor.WeekPercent }, 0, 100),
			libcli.OneOfKey(b, prefix+"on_unknown_usage", "Whether team roles carry on or wait while usage can't be read", func(c *config.Config) *string { return &engine(c).OnUnknownUsage }, []string{"allow", "pause"}),
		)
	}
	return keys
}

func boolKey(b libcli.ConfigBinding[config.Config], name, description string, field func(*config.Config) *bool) libcli.ConfigKey {
	return withValues(libcli.FieldKey(b, name, description, field, func(v string) (bool, error) {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return false, output.Newf(output.FixableByAgent, "%s must be true or false, got %q", name, v)
		}
		return parsed, nil
	}, strconv.FormatBool), "true", "false")
}

func withValues(k libcli.ConfigKey, values ...string) libcli.ConfigKey {
	k.Values = values
	return k
}
