# Knowledge, imports, and generation

Culi's canonical knowledge is a directory of Markdown cards under `~/.culi/knowledge/`. That
directory is initialized as a Git repository so imported, learned, and generated changes have a
local governance trail. SQLite is the retrieval/activity index, not the source of truth.

## Card types

The top-level directory supplies the default type and the relative path supplies the default ID:

| Path | Type | Derived ID |
|---|---|---|
| `rules/go-errors.md` | rule | `rules/go-errors` |
| `lessons/check-migrations.md` | lesson | `lessons/check-migrations` |
| `styles/commit-subject.md` | style | `styles/commit-subject` |
| `patterns/retry-backoff.md` | pattern | `patterns/retry-backoff` |
| `skills/tdd/SKILL.md` | skill | `skills/tdd` |
| `agents/security-review.md` | agent | `agents/security-review` |

## Card format

Only `title` is required. A compact card might be:

```markdown
---
title: Wrap Go errors with operation context
summary: Use %w and identify the failed operation when returning an error.
scope: [lang:go]
key: go/error-return
triggers:
  keywords: [error, wrap, fmt.Errorf]
aliases: [error handling]
baseline: false
status: confirmed
---

Wrap errors with `%w` so callers retain `errors.Is` and `errors.As` behavior.
Prefix the error with a short package and operation label.
```

Important fields:

- `scope`: one or more of `global`, `lang:<name>`, `repo:<name>`,
  `branch:<repo>@<glob>`, or an internal directory scope.
- `key`: optional shadowing identity. When cards with the same key are in scope, the narrowest
  branch/repository/language card wins.
- `triggers.keywords` and `triggers.globs`: strong retrieval hints. Culi limits forced trigger pins
  so they cannot consume the entire token budget.
- `aliases`: extra search vocabulary.
- `baseline`: eligible for the SessionStart baseline.
- `status`: empty/`confirmed`, `candidate`, or `retired`.
- `observations` and `supersedes`: learning lifecycle metadata normally managed by Culi.
- `provenance`: source, contributing repositories, model, and deterministic source hash for
  generated cards.

After a manual edit, synchronize the index:

```bash
culi index
```

Use `culi index --full` to rebuild and re-embed every card. Missing or unavailable Ollama degrades
to BM25-only retrieval; it does not invalidate the files.

## Card lifecycle

Learned cards begin as candidates. A reviewer can approve/reject them immediately, or repeated
independent observations can confirm them automatically according to `learn.confirm_at` (default
2). Unreinforced candidates expire after `learn.candidate_ttl_days` (default 30); confirmed cards
are never auto-retired.

Retirement is a status change rather than deletion. This preserves history and makes accidental
removal recoverable. `culi down <id>` lowers utility without removing the card.

## Import existing agent guidance

The import pipeline reconciles duplicated instructions across repositories:

```bash
culi import scan /path/to/repo-a /path/to/repo-b
culi import merge
# inspect ~/.culi/knowledge/.import/staged/
culi import apply
```

### Scan

`scan` is read-only. It inventories:

- `.claude/agents/*.md`;
- `.claude/skills/*/SKILL.md` plus skill attachments;
- root `CLAUDE.md`;
- active root `AGENTS.override.md` or `AGENTS.md`;
- active global Codex guidance under `CODEX_HOME`.

Nested `AGENTS.md` files are intentionally excluded until subtree scope can be preserved exactly.
The report classifies agent/skill copies as identical, superset, diverged, or unique and writes
`knowledge/.import/scan.json`.

Repositories may also be configured once:

```yaml
repos:
  - /absolute/path/to/repo-a
  - /absolute/path/to/repo-b
```

### Merge

`merge` writes only to `knowledge/.import/staged/`. Identical/superset work is mechanical;
diverged definitions and instruction decomposition can use:

- `codex-cli` or `claude-cli` with an existing terminal login;
- `openai` or `anthropic` with an API key;
- `ollama` with a local generation model;
- `none` / `--no-llm` for mechanical work only.

The import backend is separate from `learn.provider` and is selected with `import.provider`,
`--provider`, or `--no-llm`. If a long merge is interrupted, completed units remain staged and can
continue with `culi import merge --resume`.

Review the staged files before applying them:

```bash
git -C ~/.culi/knowledge diff --no-index /dev/null .import/staged
```

### Apply

`apply` is the explicit boundary that moves staged cards into canonical knowledge and reindexes
them. Existing-card conflicts remain staged unless `--force` is supplied. Repository-specific
residual instruction files are emitted for manual replacement; Culi does not overwrite those
hand-authored files automatically.

## Export Claude agents and skills

Imported agent/skill cards retain export metadata so Culi can regenerate `~/.claude/agents` and
`~/.claude/skills`:

```bash
culi export --check
culi export
```

The manifest detects hand edits. Culi keeps modified generated files and asks you to fold the change
back into the card; `--force` is required to overwrite them.

## Generate repository instructions from Git

`culi gen` collects deterministic Git facts, then uses the configured learning backend to produce
managed instruction spans and repo-scoped cards:

```bash
culi gen --repo /path/to/repo --dry-run
culi gen --repo /path/to/repo --target=both
culi gen --repo /path/to/repo --branch=feature/name
```

Targets are `claude`, `codex`, or `both`. Culi writes only inside owned marker spans, detects user
edits as conflicts, and skips regeneration when the source-facts hash is unchanged unless `--force`
is requested.
