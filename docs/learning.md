# Learning and cost

Culi's prompt path is model-free. Hooks, retrieval, ranking, and packing perform no LLM calls. Model
traffic is limited to explicit import/generation commands and the detached learning worker that
mines queued transcripts.

The learning loop:

1. A Claude or Codex lifecycle hook records a stable transcript pointer.
2. A detached `culi learn --auto` worker reads only new bytes using a persistent cursor.
3. Cheap structural gates discard sessions/windows that contain no durable correction or preference.
4. The selected backend emits schema-constrained candidate lessons and style observations.
5. Culi deduplicates/reinforces candidates, applies the confirmation policy, and exposes review work.

Terminal-model calls set `CULI_INTERNAL=1`, so Culi's own Claude/Codex sessions do not receive
injected context or re-enter the learning queue.

## Codex history and automatic recovery

Codex hook payloads may omit `transcript_path`. Culi therefore also discovers rollout files through
Codex's local `state_*.sqlite` database:

- databases are opened read-only and query-only;
- rollout paths must resolve inside `CODEX_HOME`;
- Stop-triggered recovery is throttled to once every ten minutes;
- SessionEnd requests one final unthrottled scan;
- a separate lease prevents concurrent scanners;
- stable queue IDs, byte cursors, active retries, and parked failures prevent duplicate work;
- scanner health stores only counts/timestamps/errors, never prompts, repository names, or session IDs.

Inspect the integration with:

```bash
culi doctor --harness=codex
```

Preview all discoverable history without writing or learning:

```bash
culi learn --scan-codex --dry-run
```

Force an immediate backfill and then mine according to the configured caps:

```bash
culi learn --scan-codex
```

Discovery itself makes no model calls. Mining newly queued content does.

## Providers

Set `learn.provider` in `~/.culi/config.yaml` or choose a provider in Console → Settings.

| Provider | Authentication | Billing and behavior |
|---|---|---|
| `codex-cli` | Persisted `codex login` | Uses Codex/ChatGPT account quota; ephemeral read-only `codex exec`; `$0` in Culi's API-spend ledger |
| `openai` | `OPENAI_API_KEY` or `learn.openai_api_key_file` | Metered OpenAI API; estimated USD is recorded and capped |
| `claude-cli` | Persisted Claude Code login or `learn.oauth_token_file` | Uses Claude subscription quota; `$0` in Culi's API-spend ledger |
| `anthropic` | `ANTHROPIC_API_KEY` or `learn.anthropic_api_key_file` | Metered Anthropic API; estimated USD is recorded and capped |
| `ollama` | Local HTTP endpoint | Local compute; choose local generation models explicitly |
| `none` | None | Leaves jobs queued and disables model-powered mining; retrieval remains active |

`auto` tries available backends in this order:

1. Anthropic API when an environment key or key file is configured.
2. OpenAI API when an environment key or key file is configured.
3. Claude CLI when the executable is available.
4. Codex CLI when the executable is available.

Ollama remains opt-in because an embedding model is not necessarily a suitable generation model.
Automatic CLI selection detects the executable; a signed-out backend is reported when the worker
attempts to use it and the queued jobs remain intact.

## Model defaults

| Provider family | Routine model | Schema-failure escalation |
|---|---|---|
| OpenAI API | `gpt-5.6-luna` | `gpt-5.6-terra` |
| Codex terminal | `gpt-5.6-terra` | `gpt-5.6-sol` |
| Anthropic / Claude terminal | `claude-haiku-4-5` | `claude-sonnet-5` |
| Ollama | `qwen3` recommendation | `qwen3` recommendation |

You can edit both model IDs in Settings. When switching between OpenAI and Claude provider families,
Culi replaces an obviously cross-vendor stale default; custom model names otherwise pass through to
the selected backend.

## Authentication for background workers

A persisted terminal login is normally enough:

```bash
codex login
claude auth login
```

Detached workers do not necessarily inherit environment variables defined only in an interactive
shell. If background learning reports “not logged in” while the same command works in your terminal,
use a persisted login or point Culi to a credential file:

```yaml
learn:
  oauth_token_file: ~/.claude-tokens/account.token
  anthropic_api_key_file: ~/.anthropic/api-key
  openai_api_key_file: ~/.openai/api-key
```

Each file contains only the corresponding token/key value. Culi expands `~`; secret contents are
never written to logs or the console API. OpenAI API authentication is separate from a Codex CLI
subscription login.

## Local Ollama learning

Semantic retrieval and model-powered learning use different model capabilities. Keep
`nomic-embed-text` for embeddings and install a generation model for mining:

```bash
ollama pull nomic-embed-text
ollama pull qwen3
```

```yaml
ollama:
  endpoint: http://localhost:11434
  model: nomic-embed-text

learn:
  provider: ollama
  cheap_model: qwen3
  strong_model: qwen3
```

Culi asks Ollama for schema-constrained JSON. Smaller local models can still produce noisier
judgment about what is durable; the candidate review queue remains the quality boundary.

## Hard caps

Caps reset at UTC midnight:

- `daily_usd_cap` defaults to `$0.50` and applies only to metered OpenAI/Anthropic API calls.
- `daily_call_cap` defaults to `40` and applies to every backend, including terminal subscriptions
  and Ollama.
- `max_jobs_per_run` defaults to 50 newest transcripts per worker run.

Both applicable caps must pass before a call starts. A deterministic authentication/configuration
failure does not consume the call cap and stops the batch with jobs still queued. A real model call
is recorded even when its output later fails validation because the provider already billed or
consumed quota for it.

## Manual commands

```bash
culi learn                 # drain one capped pass from the current inbox
culi learn --from-start    # ignore cursors for queued transcripts
culi learn --style         # force style synthesis now
culi learn --no-cap        # ignore daily caps/job limit and drain the backlog
culi learn --scan-codex    # discover/backfill Codex rollouts, then learn
```

`--no-cap` is intentionally explicit: it can consume substantial API spend or subscription quota.
`--auto` and `--scan-codex-force` are lifecycle-worker flags rather than normal interactive usage.

## Candidate lifecycle

- Candidates begin with one observation.
- Matching observations reinforce rather than duplicate the card.
- `confirm_at` defaults to 2 independent observations; set 1 to trust first-sighting output or a
  higher value for stricter corroboration.
- `candidate_ttl_days` defaults to 30; `-1` disables candidate expiry.
- Confirmed cards are never automatically retired by the TTL janitor.
- Superseding confirmed knowledge retires the old card instead of deleting it.

Use the console Review screen or `culi review` to decide sooner.

## Diagnostics

```bash
tail -50 ~/.culi/logs/learn.log
tail -50 ~/.culi/logs/hook.log
culi stats
culi doctor --harness=codex
```

Expected non-errors include gate skips for short/acknowledgement/non-novel sessions and throttled
Codex scans. Backend-down errors keep work queued. Scanner errors and health paths are scrubbed of
the configured Culi/Codex home prefixes before durable health reporting.
