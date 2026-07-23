# Configuration reference

Culi reads `~/.culi/config.yaml`. Set `CULI_HOME` to move the entire Culi home for tests or an
alternate profile. Missing sections use conservative defaults, so a small partial file is valid.

## Complete example

```yaml
push_budget: 700
baseline_budget: 1200

extra_acks:
  - understood
  - sounds good

extra_stopwords:
  - internal-project-term

repos:
  - /absolute/path/to/repo-a
  - /absolute/path/to/repo-b

ollama:
  endpoint: http://localhost:11434
  model: nomic-embed-text

import:
  provider: auto
  merge_model: claude-sonnet-5

learn:
  enabled: true
  provider: auto
  cheap_model: claude-haiku-4-5
  strong_model: claude-sonnet-5
  daily_usd_cap: 0.50
  daily_call_cap: 40
  max_jobs_per_run: 50
  confirm_at: 2
  candidate_ttl_days: 30
  oauth_token_file: ""
  anthropic_api_key_file: ""
  openai_api_key_file: ""
```

You only need to include values you want to override.

## Retrieval and scope

### `push_budget`

Maximum estimated tokens injected for a UserPromptSubmit event. Default: `700`. The packer leaves
headroom and the hook enforces a final output guard independently.

### `baseline_budget`

Maximum estimated tokens injected during SessionStart. Default: `1200`. Only applicable baseline
cards in the current scope are considered.

### `extra_acks`

Additional acknowledgement phrases that should skip retrieval entirely. This extends the built-in
multilingual acknowledgement gate.

### `extra_stopwords`

Additional tokens ignored while deciding whether a prompt contains useful retrieval terms.

### `repos`

Absolute repository paths watched by import and repository-learning workflows. You may edit this
list in Console → Settings. It does **not** restrict normal prompt injection; session scope comes
from the current working directory and Git root.

## Ollama

### `ollama.endpoint`

Local Ollama base URL. Default: `http://localhost:11434`.

### `ollama.model`

Embedding model used for semantic retrieval. Default: `nomic-embed-text`. Learning through Ollama
uses `learn.cheap_model` and `learn.strong_model`, not this embedding model.

If Ollama is unavailable, retrieval continues with SQLite FTS/BM25.

## Import

### `import.provider`

Backend for `culi import merge`:

`auto | codex-cli | openai | claude-cli | anthropic | ollama | none`

`auto` checks API keys from the invoking environment, then installed Claude and Codex terminals.
Unlike background learning, interactive import currently reads OpenAI/Anthropic API keys from the
environment rather than the learning key-file fields. `none` performs mechanical merge work only.

### `import.merge_model`

Model for diverged-cluster reconciliation and instruction decomposition. Default:
`claude-sonnet-5`. Provider resolution replaces an obvious cross-vendor default when switching
between Claude and OpenAI/Codex families. For Ollama, set a local generation model such as `qwen3`.

Command-line `--provider` and `--model` values override this section for one merge.

## Learning

### `learn.enabled`

Master model-powered learning switch. Default: `true`. Set `false` to stop transcript mining. Hooks
and Codex recovery may still queue transcript pointers so learning can resume later. Setting
`provider: none` has the same no-model behavior while retrieval remains active.

### `learn.provider`

`auto | codex-cli | openai | claude-cli | anthropic | ollama | none`

See [learning and cost](learning.md) for provider selection and billing behavior.

### `learn.cheap_model` / `learn.strong_model`

Routine model and one-step schema-failure escalation model. Defaults are Claude Haiku/Sonnet for
`auto`; provider resolution supplies appropriate OpenAI/Codex defaults after a provider switch.

### `learn.daily_usd_cap`

Maximum estimated metered OpenAI/Anthropic API spend per UTC day. Default: `0.50`. Terminal
providers and Ollama record `$0` in this ledger. Values `<= 0` fall back to the default; use
`provider: none` or `enabled: false` to stop model calls.

### `learn.daily_call_cap`

Maximum model calls per UTC day across every provider. Default: `40`. This protects terminal
subscription quota and local workloads as well as metered APIs. Values `<= 0` use the default.

### `learn.max_jobs_per_run`

Newest queued transcripts processed by one worker run. Default: `50`; `-1` removes the per-run
limit. `culi learn --no-cap` also bypasses this limit.

### `learn.confirm_at`

Independent observations required to promote a candidate automatically. Default: `2`; `1` confirms
on first sighting; higher values are more conservative. Values below 1 use the default.

### `learn.candidate_ttl_days`

Days before an unreinforced candidate is auto-retired. Default: `30`; `-1` disables expiry.
Confirmed cards are never auto-retired by this policy.

### Credential files

- `learn.oauth_token_file`: file containing `CLAUDE_CODE_OAUTH_TOKEN` for Claude terminal learning.
- `learn.anthropic_api_key_file`: file containing `ANTHROPIC_API_KEY`.
- `learn.openai_api_key_file`: file containing `OPENAI_API_KEY`; not used by `codex-cli`.

Paths may begin with `~`. The files should contain only the credential value and should not be
committed. Their contents are never returned by the console.

## Environment variables

| Variable | Purpose |
|---|---|
| `CULI_HOME` | Override the default `~/.culi` store/config/state location |
| `CODEX_HOME` | Override the default `~/.codex` location used for hooks and rollout discovery |
| `OPENAI_API_KEY` | OpenAI API authentication |
| `ANTHROPIC_API_KEY` | Anthropic API authentication |
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude terminal token when available to the worker process |
| `CULI_NO_LEARN_SPAWN` | Disable detached learning-worker spawning; useful for fully manual learning |

`CULI_INTERNAL` is reserved for Culi's own terminal-model subprocesses and should not be set in a
normal shell.

## Console writes

Console → Settings can write only an explicit whitelist:

- prompt/session budgets;
- candidate TTL and confirmation threshold;
- USD/call/job caps;
- learning provider and models;
- credential file paths;
- extra acknowledgements and stopwords;
- watched repositories (through its dedicated repository manager).

The YAML writer preserves comments and unknown/unlisted keys. Import settings, Ollama endpoint/model,
and `learn.enabled` remain manual configuration fields.
