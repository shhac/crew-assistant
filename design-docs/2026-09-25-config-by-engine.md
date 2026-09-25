# Config laid out by engine

2026-09-25. Built for v0.20.0, on lib-agent-cli v0.27.0.

## Why

Tasks sat on "Waiting for Claude usage to reset" after the owner raised the limit with `config set limits.role_usage.claude_max_used_percent 98`. v0.19.1 fixed the waiting itself. The owner then asked for the config to be laid out like crew-code-review's, with a section per engine, and for `config get` and `config unset` alongside `config set`. Two choices were theirs:

- **Usage limits** became floors of usage *left*, one per window (5-hour and weekly), as crew-code-review's `usage_floor` does. A single used-percent across every window had been the old setting.
- **Unknown keys** are loaded, kept and reported, as in crew-code-review, rather than refusing the file.

## What was built

- **`engines`** holds how to reach each engine, shared by the assistant, team roles, small models, the painter and the usage meter:
  - `codex` and `claude` each have `bin`, `home`, `usage_floor.{5h_percent,1w_percent}` and `on_unknown_usage`;
  - `openai-compatible` has `base_url` and `api_key_env`.
  `model` keeps only the assistant's own engine, model, effort and token limit. Every engine field is optional, and blank means the default.
- **Floors.**
  - A window of a day or less answers to the 5-hour floor; a longer one answers to the weekly floor. A window that doesn't say how long it is takes the stricter of the two.
  - A role is held while less than the floor is left. 0 turns a window's floor off, and with both off, `on_unknown_usage` is ignored too.
  - The hold names the window: "claude weekly usage has 5% left (floor 10%)".
- **Earlier files** are converted on every load and written back once, when `serve` starts, or at the next save. The conversions:
  - A `model` with no `engine` is an API config.
  - Binaries, homes and the endpoint move to `engines`.
  - A used-percent X leaves a floor of 100−X on both windows. The old default of 90 becomes the default, and 0 stays off.
  - An open dashboard's old-shape save is converted by the server too.
- **Unknown keys** survive saves, since the file is written over the stored document with lib-agent-cli's overlay store. `serve` and `doctor` name each unknown key, saying where a renamed one went.
- **`config get/set/unset/list`** come from lib-agent-cli's `ConfigCommand`, with one typed key per setting, named by its path in the file. Each change is checked against the whole config, and goes through the running daemon when there is one. `get` and `unset` also reach keys the file holds but this version doesn't know, so a stale one can be removed. Shell completion reads the same registry. The typed key builders were added to lib-agent-cli for this, so that crew-code-review can use them too.

## Not built

- **Floors per model pool.** Opus-only windows and similar take the weekly floor, and apply only to the model they cover, as before.
- **A section key for `usage_floor` as a whole.** Each window is unset on its own.
