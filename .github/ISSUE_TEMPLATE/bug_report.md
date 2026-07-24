---
name: Bug report
about: Report incorrect or unexpected behavior in culi
title: "fix: "
labels: bug
assignees: ""
---

## What happened

<!-- A clear description of the bug and what you expected instead. -->

## Affected area

<!--
Which subcommand or package? e.g. `culi hook`, `culi mcp`, `culi serve`,
`culi query`, or internal/{retrieve,pack,hook,store,knowledge,learn,indexer}.
If it's on the hot path (hook/retrieve/pack), note observed latency.
-->

## Reproduction

<!--
Minimal steps. The more self-contained, the faster the fix.
- Which harness (Claude Code / Codex)?
- Was Ollama up, or BM25-only?
- Relevant prompt or card, if it involves retrieval/injection.
-->

## Logs / error output

<!-- Paste the error and anything from ~/.culi/logs/. Redact keys and private card content. -->

```

```

## Environment

- culi version / commit (`culi version`):
- Go version (`go version`):
- OS:
- `culi doctor` output (redact as needed):
