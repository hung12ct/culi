<div align="center">
  <img src="docs/logo.png" alt="culi" width="120" height="120" />
  <h1>culi ☕</h1>
  <p><b>Local context control for Claude Code and OpenAI Codex.</b></p>
  <p>One trusted knowledge store. Only the relevant cards reach each prompt. Useful lessons come back for review.</p>
</div>

[![Release](https://img.shields.io/github/v/release/hung12ct/culi?display_name=tag&sort=semver)](https://github.com/hung12ct/culi/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Single binary](https://img.shields.io/badge/deploy-single%20static%20binary-success)](#quick-start)

**culi** (cà phê culi — the single dense peaberry bean) is the memory and context layer between
your coding agents and your projects. It stores rules, lessons, styles, patterns, skills, and agent
definitions as small Markdown cards, then routes a token-budgeted selection to Claude Code or Codex.

It is local-first, git-backed, and inspectable. Hooks and retrieval make no model calls; optional
background learning proposes new knowledge for review. No Node, Python, or resident daemon is
required.

## What you get

- **One cross-agent memory** for Claude Code and Codex, with explicit `claude:<id>` and
  `codex:<id>` provenance.
- **Small, relevant context** through hooks, with deeper on-demand retrieval through MCP.
- **Background learning** through Codex CLI, OpenAI API, Claude CLI, Anthropic API, or Ollama.
- **Codex history recovery** from its read-only local rollout database, automatically throttled and
  deduplicated when hooks do not provide a transcript path.
- **A production-style local console** for review, knowledge maintenance, delivery activity,
  Knowledge Pulse analytics, backend configuration, and light/dark themes.
- **Plain files as truth** under `~/.culi/knowledge/`; SQLite holds rebuildable card-search tables
  plus preserved local activity state.

## Why culi?

| Without culi | With culi |
|---|---|
| Guidance is copied across `CLAUDE.md`, `AGENTS.md`, agents, and skills | One canonical, git-versioned card store |
| Every session loads large instruction files | Only prompt-relevant cards are packed under a hard budget |
| Claude and Codex learn separately | Both agents contribute to and retrieve from the same knowledge |
| Useful corrections disappear in old transcripts | Lessons are mined, deduplicated, and placed in a review queue |
| Context delivery is invisible | Activity and Knowledge Pulse show what was delivered, where, and to which harness |

## Quick start

```bash
go install github.com/hung12ct/culi/cmd/culi@latest
culi init                       # auto-detect Claude Code and Codex
culi serve                      # http://localhost:7378
```

Open a new coding-agent session after initialization. Codex users should run `/hooks` once and
trust the four generated Culi hooks. Verify the wiring with:

```bash
culi doctor --harness=codex
```

Ollama is optional. Without it, retrieval uses SQLite FTS/BM25. For semantic retrieval, install
[Ollama](https://ollama.com) and run `ollama pull nomic-embed-text`.

For harness-specific setup, adding your first cards, and importing existing instructions, see the
[getting started guide](docs/getting-started.md).

## Console

The local console makes Culi's behavior visible and reversible: review proposed lessons, search and
edit cards, filter delivery activity by agent/repository/date, compare hot cards across Claude and
Codex, and safely tune the learning backend.

| Home — health, next actions, and savings | Review — evidence-first lesson decisions |
|---|---|
| ![Culi overview](docs/overview.png) | ![Culi review queue](docs/review.png) |

See the [console guide and full screenshot gallery](docs/console.md).

## How it works

```text
prompt ──► cheap gate ──► scope + keyword + optional semantic retrieval
                                      │
                              rank + budgeted pack
                                      │
                         Claude Code or Codex
                                      │
session transcript ──► background mining ──► review ──► knowledge cards
```

- **Push:** lifecycle hooks inject the best-fitting context. Any failure produces no injection and
  never blocks the coding agent.
- **Pull:** the local MCP server exposes `search_context`, `expand_card`, and `save_lesson`.
- **Learn:** detached workers mine transcripts, synthesize style, and extract durable lessons under
  daily call/spend caps.
- **Govern:** imports and generated knowledge are staged or git-backed; retiring a card is
  reversible and hand-authored files are not silently rewritten.

## Learning backends

| Provider | Authentication | Billing path |
|---|---|---|
| `codex-cli` | Existing `codex login` | ChatGPT/Codex account quota |
| `openai` | `OPENAI_API_KEY` or key file | Metered OpenAI API |
| `claude-cli` | Existing Claude Code login or token file | Claude subscription quota |
| `anthropic` | `ANTHROPIC_API_KEY` or key file | Metered Anthropic API |
| `ollama` | Local endpoint | Local compute |
| `none` | None | Learning disabled; retrieval remains active |

The default `auto` provider tries configured APIs first, then signed-in Claude and Codex terminals.
Ollama is opt-in so Culi never silently chooses an unprepared local generation model. Model calls
never occur in prompt hooks.

See [learning and cost](docs/learning.md) for Codex backfill, authentication, caps, model defaults,
and manual learning commands.

## Documentation

- [Getting started](docs/getting-started.md) — install, initialize, verify, and add knowledge.
- [Console](docs/console.md) — Home, Review, Knowledge Pulse, Activity, Settings, and themes.
- [Knowledge and imports](docs/knowledge.md) — card format, scopes, import/merge/apply, export, and git generation.
- [Learning and cost](docs/learning.md) — providers, auth, Codex recovery, caps, and troubleshooting.
- [Configuration](docs/configuration.md) — complete `config.yaml` reference and safe console settings.
- [Product vision](docs/VISION.md) — principles and future direction.

## Common commands

| Command | Purpose |
|---|---|
| `culi init [--harness=auto\|claude\|codex\|all]` | Create the store and register selected hooks + MCP |
| `culi serve` | Start the local control console |
| `culi doctor --harness=codex` | Check Codex hooks, MCP, scanner health, and learning backlog |
| `culi query <text>` | Inspect retrieval from the terminal |
| `culi stats` | Show context savings, gate behavior, and learning spend |
| `culi learn` | Mine queued transcripts once |
| `culi learn --scan-codex --dry-run` | Preview discoverable Codex history without writing |
| `culi import scan\|merge\|apply` | Reconcile existing agent instructions into cards |
| `culi gen --repo X --target=claude\|codex\|both` | Generate repo instructions and cards from git history |
| `culi review` | Approve or reject proposed cards in the terminal |

Run `culi help` for the complete command list.

## Safety contracts

- Hook failures fail open: empty output, exit 0, never block the agent.
- Prompt-path retrieval has no LLM calls and enforces its token cap twice.
- API spend and all-provider call counts have daily hard limits.
- Culi's own terminal-model calls are marked internal to prevent self-learning loops.
- Knowledge files are canonical; runtime SQLite state is migrated or rebuildable as appropriate.
- Destructive knowledge changes remain explicit and reversible.

## Requirements

- Go 1.25+ for `go install` (the Go toolchain can download the pinned version automatically).
- Claude Code with lifecycle hooks and MCP, and/or Codex CLI 0.145+.
- Ollama with `nomic-embed-text` is optional.

## Status

Actively developed and dogfooded daily. Built on
[gopheragent](https://github.com/hung12ct/gopheragent). Issues and ideas are welcome.

## License

MIT © 2026 hung12ct
