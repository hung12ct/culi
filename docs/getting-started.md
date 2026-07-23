# Getting started

This guide takes Culi from installation to its first useful context delivery.

## 1. Install

```bash
go install github.com/hung12ct/culi/cmd/culi@latest
culi version
```

Go installs the single `culi` binary into `GOBIN` (or the default Go bin directory). Culi itself
does not require Node, Python, or a resident daemon.

Requirements:

- Go 1.25+ for installation.
- Claude Code with lifecycle hooks and MCP, and/or Codex CLI 0.145+.
- Git, used to version `~/.culi/knowledge/`.
- Ollama is optional; SQLite FTS/BM25 works without it.

## 2. Connect your coding agents

```bash
culi init
```

`auto` detects installed harnesses. Choose explicitly when needed:

```bash
culi init --harness=claude
culi init --harness=codex
culi init --harness=all
```

| Harness | What initialization registers |
|---|---|
| Claude Code | SessionStart, UserPromptSubmit, and asynchronous SessionEnd hooks; user-scoped MCP; a Culi statusline when no statusline already exists |
| Codex | SessionStart, UserPromptSubmit, Stop, and SessionEnd hooks; user-scoped MCP |

Initialization is idempotent. It preserves neighboring configuration and keeps a one-time backup
before first changing Claude or Codex hook files. Use `--no-hooks` if you only want the store and
MCP registration.

### Trust Codex hooks

Codex requires an explicit review. Open a new Codex session, run `/hooks`, and trust the four Culi
entries. Culi cannot set this trust decision on your behalf.

Then verify the live integration:

```bash
culi doctor --harness=codex
```

A healthy result shows four configured hooks, the 3-second SessionEnd timeout, MCP registration,
recent `codex:<session-id>` activity, scanner readiness, and the pending learning count.

If an MCP client displays `Auth: Unsupported` for Culi, that is expected: Culi is a local stdio MCP
server and does not use a network authentication flow.

## 3. Add knowledge

You can import existing agent instructions, author cards directly, or let background learning
propose lessons.

### Import existing repositories

Add repositories in Console → Settings, in `~/.culi/config.yaml`, or pass them to the scan command:

```bash
culi import scan /path/to/repo-a /path/to/repo-b
culi import merge
culi import apply
```

The pipeline discovers `CLAUDE.md`, `AGENTS.md`, `.claude/agents`, and `.claude/skills` and stages
the merge. Nothing enters canonical knowledge until you inspect it and explicitly run `apply`. See
[knowledge and imports](knowledge.md).

### Create a card directly

Create `~/.culi/knowledge/rules/go-error-wrapping.md`:

```markdown
---
title: Wrap Go errors with operation context
summary: Use %w and name the failed operation when returning an error.
scope: [lang:go]
triggers:
  keywords: [error, wrap, fmt.Errorf]
---

Wrap errors with `%w` so callers can still use `errors.Is` and `errors.As`.
Include a short operation label, for example `config: loading file: %w`.
```

Then sync the file store into the search index:

```bash
culi index
culi query "how should I return this Go error?"
```

The filename derives the card ID (`rules/go-error-wrapping`) and its directory derives the type.
Only `title` is required; Culi defaults missing scope to `global`.

## 4. Open the console

```bash
culi serve
```

Open http://localhost:7378. The console shows current health, review work, searchable knowledge,
delivery analytics, Claude/Codex activity, learning providers, and safe configuration controls.
It supports light and dark themes and stores the preference in the browser.

See the [console guide](console.md) for each screen.

## 5. Let Culi learn

Learning runs after lifecycle events in a detached process. Prompt hooks never call a model. Pick a
backend in Console → Settings or configure `learn.provider`:

- `codex-cli` reuses `codex login`.
- `claude-cli` reuses the Claude Code login.
- `openai` and `anthropic` use metered API keys.
- `ollama` runs locally.
- `none` disables model-powered mining without disabling retrieval.

Run one queued pass manually with:

```bash
culi learn
```

Codex users can preview or backfill older rollouts:

```bash
culi learn --scan-codex --dry-run
culi learn --scan-codex
```

See [learning and cost](learning.md) before changing caps or authentication.

## Daily workflow

Most days Culi is automatic. Useful manual commands are:

```bash
culi serve                 # inspect and review
culi stats                 # context and learning economics
culi query "topic"         # debug retrieval
culi review                # terminal review queue
culi doctor --harness=codex
```

Knowledge remains in `~/.culi/knowledge/`; logs are under `~/.culi/logs/`; the learning inbox and
content-free scanner state live under `~/.culi/inbox/` and `~/.culi/state/`.

## Troubleshooting

- **Codex hooks show Review:** run `/hooks` and trust each Culi entry.
- **SessionEnd timeout needs repair:** rerun `culi init --harness=codex`; Culi reconciles its own
  timeout without replacing neighboring hooks.
- **No semantic search:** check Ollama and `nomic-embed-text`; BM25 remains available while Ollama
  is down.
- **Background learning says not logged in:** use a persisted terminal login or a configured key
  file; shell-only environment variables may not reach detached workers.
- **`Last scan: never`:** submit/finish a Codex turn, or run the dry-run command to confirm history
  discovery. The first lifecycle worker records scanner health.
- **MCP tools are listed but Auth is unsupported:** expected for the local stdio transport.
