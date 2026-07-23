# CLAUDE.md — Culi

> **Culi** (cà phê culi — the single dense peaberry bean; sibling of [Phin](https://github.com/hung12ct/phin))
> is a **context orchestrator for Claude Code and OpenAI Codex**: one canonical knowledge store of
> small "cards" (rules / skills / lessons / styles / patterns), token-budgeted context injection via
> harness hooks, on-demand depth via an MCP server, and background learning from transcripts + git history.
> Built on **gopheragent**. Pure Go, one binary, no CGO.
>
> **Product goal:** kill per-repo CLAUDE.md/skill duplication and inject *only* the most relevant
> context per prompt — small, dense, concentrated. Like the bean.

Full approved design: `~/.claude/plans/right-now-for-every-zazzy-graham.md` (retrieval funnel,
schemas, learning pipelines, phased roadmap).

## Architecture

- **One binary `culi`**, subcommand dispatch in `cmd/culi/main.go` (stdlib `flag`, no cobra).
- **Files are truth**: `~/.culi/knowledge/` (git-init'd markdown cards with YAML frontmatter) is the
  source of truth; `~/.culi/index.db` (SQLite via `modernc.org/sqlite`: FTS5 BM25 + embedding blobs)
  is disposable — schema change ⇒ drop & rebuild, never ALTER migrations.
- **Push**: `culi hook <event>` — normalized Claude/Codex UserPromptSubmit, SessionStart, Stop, and SessionEnd adapters.
- **Pull**: `culi mcp` — stdio MCP server (`modelcontextprotocol/go-sdk`): `search_context`,
  `expand_card`, `save_lesson`.
- **Learning** (`internal/learn/`): Claude/Codex transcript mining, style synthesis, branch→CLAUDE.md/AGENTS.md generation,
  cross-branch patterns — Haiku for routine steps, Sonnet escalation, hard daily USD cap.
- **Embeddings**: local Ollama (nomic-embed-text) behind gopheragent's `tools.Embedder`; every path
  degrades gracefully to BM25-only when Ollama is down.
- **Review console**: `culi serve` — local `net/http` server (`internal/serve/`) serving an embedded
  vanilla-JS dashboard (`embed.FS`, no build step): overview, candidate review, knowledge base +
  search, injection activity (per-event card breakdown + repo attribution), settings/repos manager.
  Off the hot path, so stdlib-only (C2) does not apply. Read-mostly; card actions (down/retire/
  remove/edit) and config/repos writes are git-backed and respect C4 (retire = reversible status
  flip; hand-authored cards never round-trip through Render).

## The non-negotiable contracts

1. **Hooks fail open.** The hook path runs on *every* user prompt. Any internal error ⇒ empty
   output, exit 0, log to `~/.culi/logs/`. Never exit 2, never print garbage, never block Claude.
2. **Hot path is fast and dependency-light.** `hook`/`store`/`knowledge`/`retrieve`/`pack`/
   `session`/`embed` = stdlib + modernc + yaml only (gopheragent allowed only in
   `importer`/`learn`/the `Embedder` interface). Hard latency ceiling 150ms; p95 target <100ms.
3. **Token budget is a hard cap, enforced twice.** Packer packs to budget (700 delta / 1200
   baseline); `internal/hook` independently enforces the 10,000-char hook API cap as final guard.
4. **Never destroy user content.** Generated files carry `<!-- culi:begin/end -->` markers or
   `GENERATED` headers; hand-edited content inside them is never silently overwritten
   (conflict → `culi review`). LLM merges land in staging (`knowledge/.import/staged/`), applied
   only after review.

## Go standard (Hung's "fast and tiny")

Errors wrapped with package prefix (`fmt.Errorf("retrieve: …: %w", err)`); `ctx` first arg on I/O;
release locks before I/O / LLM calls; no panic on expected errors; stdlib over deps; functions
~80 LOC soft cap; small consumer-side interfaces; no testify — stdlib `testing` only; everything
touching goroutines passes `-race`. Same design family as gopheragent
(`~/Documents/PracticeProjects/gopheragent`) — the user authors it: flag genuine framework gaps as
upstream candidates (see plan §upstream) instead of silently building them here.

## Build & run

```bash
make check      # fmt + vet + lint + build + test (pre-commit gate)
make build      # compile ./... 
make test-race  # tests with race detector
go run ./cmd/culi query "some prompt"   # debug retrieval from terminal
```

Prereqs: Go ≥1.21 (go.mod pins 1.25; the toolchain auto-downloads), Ollama with `nomic-embed-text` pulled (optional — BM25 works without it).
