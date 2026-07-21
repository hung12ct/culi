<div align="center">
  <img src="docs/logo.png" alt="culi" width="120" height="120" />
  <h1>culi ☕</h1>
  <p><b>Context orchestrator for Claude Code</b> — one canonical knowledge store, injected <i>only</i> when it's relevant, so every prompt gets the right rules without paying to dump your whole <code>CLAUDE.md</code> every time.</p>
</div>

[![Release](https://img.shields.io/badge/release-v0.1.0%20%E2%80%94%20Peaberry-3ec7bb)](https://github.com/hung12ct/culi/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Single binary](https://img.shields.io/badge/deploy-single%20static%20binary-success)](#install)

**culi** (cà phê culi — the single dense peaberry bean) keeps all your Claude Code context — rules, skills, lessons, styles, patterns — as small markdown "cards" in one place. On every prompt it pushes just the most relevant, token-budgeted slice into Claude via hooks, lets Claude pull more on demand via an MCP server, and quietly **learns** new lessons from your sessions.

In short: **self-improving agent memory and context engineering for Claude Code** — the memory-and-context layer that plugs into its harness and decides what Claude should know at each step, so you stop re-teaching it and stop paying for context it doesn't need.

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

<!-- Add your images to a docs/ folder and they'll render below.
     Suggested: run `culi serve` and screenshot each screen. -->

| Overview | Review queue |
|---|---|
| ![Overview](docs/overview.png) | ![Review](docs/review.png) |

| Knowledge base | Activity — what culi injected, per conversation |
|---|---|
| ![Knowledge base](docs/knowledge-base.png) | ![Activity](docs/activity.png) |

---

## Install

```bash
go install github.com/hung12ct/culi/cmd/culi@latest
culi init      # creates ~/.culi, registers the hook + MCP server in Claude Code
```

That's it. Open Claude Code in any repo and culi starts injecting context. Nothing else to run.

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

- **Push** — a Claude Code *hook* runs on each prompt, retrieves the best-matching cards, and injects a budgeted block. Fails open: any error → inject nothing, never blocks you.
- **Pull** — an *MCP server* gives Claude three tools: `search_context`, `expand_card`, and `save_lesson` (tell Claude "remember this" and it writes a card — and folds it into an existing one instead of duplicating).
- **Learn** — after a session ends, culi mines the transcript in the background for lessons and missing rules, dedupes them against what it already knows, and queues them for your review.
- **Files are truth** — everything lives as plain markdown in `~/.culi/knowledge/` (git-init'd). The search index is disposable and rebuilds from the files.

---

## The review console

```bash
culi serve      # → http://localhost:7378
```

A local web UI to see and steer everything culi does: a health **overview** with real token-savings, a **review** queue for mined candidate cards, a searchable **knowledge base**, and an **activity** log showing exactly which cards Claude saw in each conversation (and why).

---

## Learning & cost

The **hot path is free** — hooks, retrieval, and packing make *zero* LLM calls. Only
**learning** talks to a model: it mines your session transcripts into candidate lessons/rules,
synthesizes coding style, and turns git history into `CLAUDE.md` + repo cards. It runs
**in the background after a session ends** (via the session-end hook) and can be run by hand
with `culi learn`.

### Backends & cost

Pick a backend with `learn.provider` (default `auto`):

| Provider | Cost | How | Notes |
|---|---|---|---|
| `claude-cli` | **Free** | Shells out to `claude -p` on your Claude Code subscription | `$0.00` in the spend meter |
| `anthropic` | **Paid** (metered) | Anthropic API via [gopheragent](https://github.com/hung12ct/gopheragent) + `ANTHROPIC_API_KEY` | Tracks real USD; capped by `daily_usd_cap` |
| `ollama` | **Free** (local) | Local models — set `cheap_model`/`strong_model` to non-Claude models | Runs on your machine |
| `none` | — | Disabled | Mining queues but never calls a model |

`auto` prefers the Anthropic API when a key is available (env **or** `anthropic_api_key_file`),
otherwise falls back to the free `claude-cli`.

### Spend caps (hard limits)

Both must pass before any call; they reset at **UTC midnight**:

- **`daily_usd_cap`** (default `$0.50`) — bounds estimated API spend. Only bites on the
  `anthropic` backend; `claude-cli`/`ollama` cost `$0`.
- **`daily_call_cap`** (default `40`) — bounds model calls on *every* backend — the
  subscription-quota guard.

A deterministic auth/config failure (logged-out CLI, bad key) does **not** count against the
cap — it halts the run and keeps jobs queued, so a misconfig can't silently drain your budget.

### Auth for background (headless) learning

When learning runs from the hook, Claude Code does **not** pass its OAuth token (or an API key)
to the spawned process — so background mining gets "Not logged in". Point culi at a credential
**file** (secrets are read from the file and never logged; `~` expands):

```yaml
learn:
  # subscription (free): a file holding CLAUDE_CODE_OAUTH_TOKEN
  oauth_token_file: ~/.claude-tokens/account.token
  # OR metered API (paid): a file holding ANTHROPIC_API_KEY
  # anthropic_api_key_file: ~/.anthropic/api-key
```

Running `culi learn` **manually** from your terminal needs neither — it inherits the token
from your shell environment.

### Running it manually

```bash
culi learn              # mine queued transcripts once, print results
culi learn --from-start # ignore cursors, re-mine every transcript from scratch
culi learn --style      # force style synthesis now (bypass the trigger policy)
culi learn --auto       # background mode (quiet, logs to ~/.culi/logs/learn.log) — used by the hook
```

Full config (all optional; shown with defaults):

```yaml
learn:
  enabled: true
  provider: auto              # auto | anthropic | claude-cli | ollama | none
  cheap_model: claude-haiku-4-5
  strong_model: claude-sonnet-5
  daily_usd_cap: 0.50         # anthropic backend only
  daily_call_cap: 40          # all backends
  candidate_ttl_days: 30      # auto-retire unreviewed candidates; -1 disables
  oauth_token_file: ""        # headless subscription auth (see above)
  anthropic_api_key_file: ""  # headless API auth (see above)
```

---

## Commands

| Command | What it does |
|---|---|
| `culi init` | Set up `~/.culi`, register hooks + MCP in Claude Code |
| `culi serve` | Local web review console (default `localhost:7378`) |
| `culi query <text>` | Debug retrieval from the terminal |
| `culi stats` | Token accounting, gate economics, learning spend |
| `culi import scan\|merge\|apply` | Reconcile drifted `.claude` dirs into the store |
| `culi export` | Regenerate `~/.claude` agents/skills from cards |
| `culi learn` | Mine queued session transcripts into candidate cards |
| `culi review` | Approve/reject mined candidates |
| `culi gen --repo X` | Turn git history into `CLAUDE.md` + repo cards |
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
- **Claude Code** (hooks + MCP)
- **Ollama** with `nomic-embed-text` — *optional*, for semantic search

---

## Status

Actively developed and dogfooded daily. Built on [gopheragent](https://github.com/hung12ct/gopheragent).
Issues and ideas welcome.

## License

MIT © 2026 hung12ct
