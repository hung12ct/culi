<div align="center">
  <img src="docs/logo.png" alt="culi" width="120" height="120" />
  <h1>culi ☕</h1>
  <p><b>Context orchestrator for Claude Code</b> — one canonical knowledge store, injected <i>only</i> when it's relevant, so every prompt gets the right rules without paying to dump your whole <code>CLAUDE.md</code> every time.</p>
</div>

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
