<div align="center">
  <img src="docs/logo.png" alt="culi" width="120" height="120" />
  <h1>culi ☕</h1>
  <p><b>Context orchestrator for Claude Code and OpenAI Codex</b> — one canonical knowledge store, injected <i>only</i> when it's relevant, so every prompt gets the right rules without paying to dump every instruction file each time.</p>
</div>

[![Release](https://img.shields.io/badge/release-v0.1.0%20%E2%80%94%20Peaberry-3ec7bb)](https://github.com/hung12ct/culi/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Single binary](https://img.shields.io/badge/deploy-single%20static%20binary-success)](#install)

**culi** (cà phê culi — the single dense peaberry bean) keeps Claude Code and Codex context — rules, skills, lessons, styles, patterns — as small markdown "cards" in one place. On every prompt it pushes the most relevant token-budgeted slice through harness hooks, exposes deeper retrieval through MCP, and quietly **learns** from both transcript formats.

In short: **self-improving agent memory and context engineering for Claude Code and Codex** — the memory-and-context layer that decides what each agent should know at each step, so you stop re-teaching it and stop paying for context it doesn't need.

No Node, no Python, no daemon required. One static Go binary.

---

## Why culi?

If you use Claude Code across more than one repo, you probably have this problem:

| Without culi | With culi |
|---|---|
| The same `CLAUDE.md`, agents & skills hand-copied into every repo | **One** canonical store, git-versioned |
| Copies drift — each repo slowly diverges | Drift is impossible — repos generate from cards |
| Every session dumps the *entire* `CLAUDE.md`, relevant or not | Only the cards that match *this* prompt are injected, under a hard token budget |
| You re-teach Claude the same lesson every week | culi mines your transcripts and remembers it |

`culi stats` shows the payoff directly — tokens injected vs. "dump everything every session."

---

## Screenshots

The `culi serve` context control console — system health, next actions, real token-savings, and a review queue for
mined candidates, a searchable knowledge base, a per-conversation injection log, and editable settings.

| Overview — token savings & card health | Review — approve/reject mined candidates |
|---|---|
| ![Overview](docs/overview.png) | ![Review](docs/review.png) |

| Knowledge base — searchable card store | Activity — exactly which cards each agent saw, per conversation |
|---|---|
| ![Knowledge base](docs/knowledge-base.png) | ![Activity](docs/activity.png) |

| Settings — safe knobs, written straight to `config.yaml` | |
|---|---|
| ![Settings](docs/settings.png) | |

---

## Install

```bash
go install github.com/hung12ct/culi/cmd/culi@latest
culi init      # auto-detects Claude/Codex and registers hooks + MCP
```

That's it. Open a new Claude Code or Codex session in any repo and culi starts injecting context. For Codex, run `/hooks` once to review and trust the generated hook definitions.

> Optional: install [Ollama](https://ollama.com) and `ollama pull nomic-embed-text` for semantic
> retrieval. culi works without it (keyword/BM25) and degrades gracefully if it's ever down.

---

## How it works

```
your prompt ─► [gate: skip acks/pastes] ─► scope + keyword + semantic match
                                                     │
                                          rank · token-budget pack
                                                     │
                                     ◄── inject only what fits ──►  Claude
```

- **Push** — a Claude Code or Codex *hook* runs on each prompt, retrieves the best-matching cards, and injects a budgeted block. Fails open: any error → inject nothing, never blocks you.
- **Pull** — an *MCP server* gives either harness three tools: `search_context`, `expand_card`, and `save_lesson`.
- **Learn** — after a session ends, culi mines the transcript in the background for lessons and missing rules, dedupes them against what it already knows, and queues them for your review.
- **Files are truth** — everything lives as plain markdown in `~/.culi/knowledge/` (git-init'd). The search index is disposable and rebuilds from the files.

---

## Context control console

```bash
culi serve      # → http://localhost:7378
```

A local web UI to see and steer everything Culi does: system health and next actions, an evidence-first **review** queue, searchable **knowledge**, and an **activity** trace showing exactly which cards Claude or Codex received and why. The product direction is documented in [docs/VISION.md](docs/VISION.md).

---

## Learning & cost

The **hot path is free** — hooks, retrieval, and packing make *zero* LLM calls. Only
**learning** talks to a model: it mines your session transcripts into candidate lessons/rules,
synthesizes coding style, and turns git history into `CLAUDE.md` + repo cards. It runs
**in the background after a session ends** (via the session-end hook) and can be run by hand
with `culi learn`.

Codex history can also be discovered directly from its read-only local state database. Preview
what Culi can see with `culi learn --scan-codex --dry-run`, then run
`culi learn --scan-codex` to queue current and older rollout transcripts. Existing byte cursors
are reused, so later scans process only newly appended rollout content.

### Backends & cost

Pick a backend with `learn.provider` (default `auto`):

| Provider | Cost | How | Notes |
|---|---|---|---|
| `codex-cli` | **Account quota** | Runs an ephemeral, read-only `codex exec` with your existing Codex login | No separate `OPENAI_API_KEY`; `$0.00` in Culi's spend meter |
| `openai` | **Paid** (metered) | OpenAI API + `OPENAI_API_KEY` | Structured output; tracked and capped by `daily_usd_cap` |
| `claude-cli` | **Free** | Shells out to `claude -p` on your Claude Code subscription | `$0.00` in the spend meter |
| `anthropic` | **Paid** (metered) | Anthropic API via [gopheragent](https://github.com/hung12ct/gopheragent) + `ANTHROPIC_API_KEY` | Tracks real USD; capped by `daily_usd_cap` |
| `ollama` | **Free** (local) | Local models — set `cheap_model`/`strong_model` to non-Claude models | Runs on your machine |
| `none` | — | Disabled | Mining queues but never calls a model |

`auto` keeps existing behavior and chooses the first ready backend in this order: Anthropic API,
OpenAI API, Claude terminal, then Codex terminal. API credentials may come from the environment or
their configured key file. Ollama remains opt-in so Culi never silently selects an unprepared local
generation model.

For OpenAI, Culi starts routine mining with `gpt-5.6-luna` and escalates schema failures to
`gpt-5.6-terra`; the Codex terminal starts with `gpt-5.6-terra` and escalates to `gpt-5.6-sol`.
These follow OpenAI's current [model catalog](https://developers.openai.com/api/docs/models) and
can be changed under Settings → Learning backend.

### Local models (Ollama) — free, private, no auth

The `ollama` backend sidesteps credentials entirely: mining runs on a local model, nothing leaves
your machine, and there's no token/key/Keychain to manage. culi enforces the JSON schema
server-side (Ollama's `format`), so structured output is reliable. Two steps:

```bash
ollama pull qwen3          # a general instruct model (not the embedding model)
```
```yaml
learn:
  provider: ollama
  cheap_model: qwen3       # routine mining (must be a non-Claude model)
  strong_model: qwen3      # escalation on schema failure
ollama:
  endpoint: http://localhost:11434   # same server as embeddings (default)
```

Trade-off: local models have weaker judgment than Claude about what's a *durable* lesson, so
expect noisier candidates — your review queue is the quality gate. Speed is GPU-bound, and the
`--no-cap` parallel drain helps less here (one Ollama instance tends to queue concurrent requests).

### Spend caps (hard limits)

Both must pass before any call; they reset at **UTC midnight**:

- **`daily_usd_cap`** (default `$0.50`) — bounds estimated API spend. It applies to the
  `openai` and `anthropic` backends; terminal providers and Ollama cost `$0` in Culi's ledger.
- **`daily_call_cap`** (default `40`) — bounds model calls on *every* backend — the
  subscription-quota guard.

A deterministic auth/config failure (logged-out CLI, bad key) does **not** count against the
cap — it halts the run and keeps jobs queued, so a misconfig can't silently drain your budget.

### Auth

Background learning runs its selected backend from a *detached* hook process, so it needs a
credential that process can read. **A normal, persisted terminal login is enough** — it lives on
disk, so every process (including the background worker) uses it:

- **Codex / ChatGPT subscription** — run `codex login`, then choose `codex-cli`. Culi invokes an
  ephemeral, read-only Codex process and marks it internal, preventing learning calls from being
  re-ingested as user sessions.
- **Claude subscription** — run `claude auth login`, then choose `claude-cli`.
- **API key** — set `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` where detached processes can see it,
  or use the corresponding key-file setting. API use is separate from terminal subscription auth.

Manual `culi learn` from your terminal always works too — it inherits whatever your shell has.

**The one gotcha is shell-only auth.** If your *only* credential is an env var exported in your
shell (e.g. `CLAUDE_CODE_OAUTH_TOKEN` in `.zshrc`, with no `claude auth login`), the detached hook
process doesn't inherit it — background mining fails with "Not logged in" even though your terminal
works. Two fixes:

1. **Do a persisted `claude auth login`** (or put the key in a system-wide env) so the credential
   is on disk rather than a shell-only variable. Simplest for most people.
2. **Point culi at a credential file** (fallback; secrets are read from the file, never logged;
   `~` expands):

   ```yaml
   learn:
     oauth_token_file: ~/.claude-tokens/account.token  # holds CLAUDE_CODE_OAUTH_TOKEN
     # anthropic_api_key_file: ~/.anthropic/api-key     # OR holds ANTHROPIC_API_KEY
     # openai_api_key_file: ~/.openai/api-key           # OR holds OPENAI_API_KEY
   ```

   Useful when you keep credentials in files, or want learning to use a *different* account than
   your interactive session.

### Running it manually

```bash
culi learn              # mine queued transcripts once, print results
culi learn --no-cap     # ignore the daily caps and drain the whole backlog in one run
culi learn --from-start # ignore cursors, re-mine every transcript from scratch
culi learn --style      # force style synthesis now (bypass the trigger policy)
culi learn --auto       # background mode (quiet, logs to ~/.culi/logs/learn.log) — used by the hook
```

Full config (all optional; shown with defaults):

```yaml
learn:
  enabled: true
  provider: auto              # auto | codex-cli | openai | claude-cli | anthropic | ollama | none
  cheap_model: claude-haiku-4-5
  strong_model: claude-sonnet-5
  daily_usd_cap: 0.50         # OpenAI/Anthropic API backends
  daily_call_cap: 40          # all backends
  max_jobs_per_run: 50        # transcripts mined per run, newest first; -1 = no limit
  confirm_at: 2               # observations to auto-confirm a candidate; 1 = on first sighting
  candidate_ttl_days: 30      # auto-retire unreviewed candidates; -1 disables
  oauth_token_file: ""        # headless subscription auth (see above)
  anthropic_api_key_file: ""  # optional ANTHROPIC_API_KEY file
  openai_api_key_file: ""     # optional OPENAI_API_KEY file; not used by codex-cli
```

---

## Commands

| Command | What it does |
|---|---|
| `culi init [--harness=auto\|claude\|codex\|all]` | Set up `~/.culi`, register selected hooks + MCP |
| `culi doctor [--harness=codex]` | Verify Codex hooks/MCP, timeout alignment, recent activity, and pending learning |
| `culi learn --scan-codex [--dry-run]` | Discover and backfill Codex rollout history (`--dry-run` only lists it) |
| `culi serve` | Local context control console (default `localhost:7378`) |
| `culi query <text>` | Debug retrieval from the terminal |
| `culi stats` | Token accounting, gate economics, learning spend |
| `culi import scan\|merge\|apply` | Reconcile `.claude`, CLAUDE.md, and root/global AGENTS.md guidance |
| `culi export` | Regenerate `~/.claude` agents/skills from cards |
| `culi learn` | Mine queued session transcripts into candidate cards |
| `culi review` | Approve/reject mined candidates |
| `culi gen --repo X --target=claude\|codex\|both` | Turn git history into instruction spans + repo cards |
| `culi card list\|show\|rm` | Inspect the card store |

---

## What makes it different

- **Token budget is a hard cap**, enforced twice — culi never blows past your limit.
- **Nothing heavy on the prompt path** — no LLM calls, stdlib-only hot path, sub-100ms p95.
- **Learning has spend caps** — daily USD and call limits, so it never eats your API budget or subscription quota.
- **One static binary** (`CGO_ENABLED=0`) — no runtime zoo to break on the next OS update.
- **Your content is never destroyed** — retiring a card is a reversible status flip; hand-authored files are never rewritten.

---

## Requirements

- **Go 1.25+** to install (the toolchain auto-downloads if needed)
- **Claude Code and/or Codex CLI 0.145+** (hooks + MCP)
- **Ollama** with `nomic-embed-text` — *optional*, for semantic search

---

## Status

Actively developed and dogfooded daily. Built on [gopheragent](https://github.com/hung12ct/gopheragent).
Issues and ideas welcome.

## License

MIT © 2026 hung12ct
