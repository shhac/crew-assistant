package toolkit

// Tool is one CLI in the family: its Homebrew formula, the skill that
// teaches agents to use it, and how the owner signs it in.
type Tool struct {
	// ID is both the binary's name and the formula's last part.
	ID      string
	Name    string
	Purpose string
	Formula string
	// Skill is its name in shhac/agent-skills, or "" for none.
	Skill string
	// Setup is shown for the owner's own terminal: signing in is
	// interactive and never runs from the daemon.
	Setup []string
	// Verify checks the sign-in without prompting; its first word is the
	// program. Nil when there's no such check.
	Verify []string
	// Connection says crew-assistant can read through it as a connection.
	Connection bool
}

const tap = "shhac/tap/"

// The connection tools come first, as the ones crew-assistant itself uses.
var catalog = []Tool{
	{ID: "lin", Name: "Linear", Purpose: "Find, create and update Linear issues, projects, cycles, documents and comments", Formula: tap + "lin", Skill: "lin",
		Setup: []string{"lin auth login <api-key>"}, Verify: []string{"lin", "user", "me"}, Connection: true},
	{ID: "agent-slack", Name: "Slack", Purpose: "Read and act on Slack: threads, search, unreads and sending messages", Formula: tap + "agent-slack", Skill: "agent-slack",
		Setup: []string{"agent-slack auth import-desktop"}, Verify: []string{"agent-slack", "auth", "test"}, Connection: true},
	{ID: "agent-notion", Name: "Notion", Purpose: "Search, read and edit Notion pages, databases, blocks and comments", Formula: tap + "agent-notion", Skill: "agent-notion",
		Setup: []string{"agent-notion auth import-desktop"}, Verify: []string{"agent-notion", "auth", "status"}, Connection: true},
	{ID: "agent-fathom", Name: "Fathom", Purpose: "Search and read Fathom meetings: transcripts, summaries and action items", Formula: tap + "agent-fathom", Skill: "agent-fathom",
		Setup: []string{"agent-fathom auth add <profile> --form"}, Verify: []string{"agent-fathom", "auth", "check"}, Connection: true},
	{ID: "agent-mongo", Name: "MongoDB", Purpose: "Read-only MongoDB: explore schemas and indexes, and query documents", Formula: tap + "agent-mongo", Skill: "agent-mongo",
		Setup: []string{`agent-mongo connection add <name> "<mongodb-uri>"`}, Verify: []string{"agent-mongo", "connection", "test"}},
	{ID: "agent-sql", Name: "SQL", Purpose: "Read-only-by-default SQL for Postgres, MySQL, SQLite, DuckDB, Snowflake and MSSQL", Formula: tap + "agent-sql", Skill: "agent-sql",
		Setup: []string{"agent-sql credential add <credential> --username <user> --form", "agent-sql connection add <name> <url> --credential <credential>"}, Verify: []string{"agent-sql", "connection", "test"}},
	{ID: "agent-dd", Name: "Datadog", Purpose: "Triage Datadog monitors, logs, metrics, traces and SLOs, and tune monitors", Formula: tap + "agent-dd", Skill: "agent-dd",
		Setup: []string{"agent-dd org add <alias> --form --site datadoghq.com"}, Verify: []string{"agent-dd", "org", "test"}},
	{ID: "agent-incident", Name: "incident.io", Purpose: "Triage incident.io incidents, alerts, on-call schedules and status pages", Formula: tap + "agent-incident", Skill: "agent-incident",
		Setup: []string{"agent-incident auth add <alias> --form"}, Verify: []string{"agent-incident", "auth", "check"}},
	{ID: "agent-posthog", Name: "PostHog", Purpose: "Investigate PostHog persons, events, HogQL, flags, dashboards and recordings", Formula: tap + "agent-posthog", Skill: "agent-posthog",
		Setup: []string{"agent-posthog auth add <profile> --form", "agent-posthog auth update <profile> --org <org-id> --project <project-id> --default"}, Verify: []string{"agent-posthog", "auth", "check"}},
	{ID: "agent-stripe", Name: "Stripe", Purpose: "Triage Stripe payments, invoices, subscriptions, disputes and payouts", Formula: tap + "agent-stripe", Skill: "agent-stripe",
		Setup: []string{"agent-stripe auth add <profile> --form"}, Verify: []string{"agent-stripe", "auth", "check"}},
	{ID: "agent-vercel", Name: "Vercel", Purpose: "Triage Vercel deployments, build and runtime logs, env vars, domains and spend", Formula: tap + "agent-vercel", Skill: "agent-vercel",
		Setup: []string{"agent-vercel auth add --form"}, Verify: []string{"agent-vercel", "auth", "test"}},
	{ID: "agent-cloudflare", Name: "Cloudflare", Purpose: "Read Cloudflare zones, DNS, SSL, WAF, cache, Workers, R2 and analytics", Formula: tap + "agent-cloudflare", Skill: "agent-cloudflare",
		Setup: []string{"agent-cloudflare auth add <profile> --form --account-id <account-id>"}, Verify: []string{"agent-cloudflare", "auth", "check"}},
	{ID: "agent-statsig", Name: "Statsig", Purpose: "Manage Statsig feature gates, dynamic configs, experiments and segments", Formula: tap + "agent-statsig", Skill: "agent-statsig",
		Setup: []string{"agent-statsig project add <name> --form"}, Verify: []string{"agent-statsig", "project", "test"}},
	{ID: "agent-postmark", Name: "Postmark", Purpose: "Triage Postmark email delivery, bounces, suppressions, domains and streams", Formula: tap + "agent-postmark", Skill: "agent-postmark",
		Setup: []string{"agent-postmark profiles setup <profile> --form --account-token --server <alias>:<server-id>:outbound"}, Verify: []string{"agent-postmark", "profiles", "check"}},
	{ID: "agent-dlocal", Name: "dLocal", Purpose: "Investigate dLocal payins, payouts, refunds, chargebacks and failures", Formula: tap + "agent-dlocal", Skill: "agent-dlocal",
		Setup: []string{"agent-dlocal auth add <profile> --form"}, Verify: []string{"agent-dlocal", "auth", "check"}},
	{ID: "agent-deepweb", Name: "Deep web", Purpose: "Make signed-in HTTP and GraphQL calls through named profiles without exposing secrets", Formula: tap + "agent-deepweb", Skill: "agent-deepweb",
		Setup: []string{"agent-deepweb profile add <name> --type <type> --domain <host>"}},
	{ID: "agent-motion", Name: "Motion", Purpose: "Describe what happens over time in a screen recording: timeline and frames", Formula: tap + "agent-motion", Skill: "agent-motion"},
	{ID: "agent-mcp-host", Name: "MCP host", Purpose: "Serve several agent-* MCP servers behind one HTTPS origin with OAuth", Formula: tap + "agent-mcp-host", Skill: "agent-mcp-host",
		Setup: []string{"agent-mcp-host pair add <name>"}},
	{ID: "g2g", Name: "g2g", Purpose: "Record stacked branches, restack them and publish them as GitHub pull requests", Formula: tap + "g2g", Skill: "g2g",
		Setup: []string{"gh auth login"}, Verify: []string{"gh", "auth", "status"}},
	{ID: "git-hunk", Name: "git-hunk", Purpose: "Stage, unstage or stash diff hunks by hash, without an interactive prompt", Formula: tap + "git-hunk", Skill: "git-hunk"},
}

// Catalog is every tool the toolkit knows, in the order it shows them.
func Catalog() []Tool {
	return append([]Tool{}, catalog...)
}

func lookup(id string) (Tool, bool) {
	for _, tool := range catalog {
		if tool.ID == id {
			return tool, true
		}
	}
	return Tool{}, false
}
