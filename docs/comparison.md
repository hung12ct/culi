# culi and claude-mem

claude-mem is the most popular memory layer for Claude Code. The two tools solve overlapping
problems with different designs. This page compares them on facts you can check, and says where
claude-mem is the better choice.

Facts about claude-mem come from its own package and README at
[v15.9.0](https://github.com/thedotmack/claude-mem/tree/v15.9.0), current as of 2026-11-03. The
[benchmark](#memory-benchmark) below installed both tools side by side in isolated homes.

## Design in one sentence each

- **claude-mem** records what happens in a session (tool calls, results, summaries), compresses it
  with a model, and injects relevant history into future sessions.
- **culi** keeps a curated store of small Markdown cards (rules, lessons, styles, patterns) and
  injects only the cards relevant to each prompt. A background learner proposes new cards that you
  approve.

## Side by side

| | culi | claude-mem 15.9.0 |
|---|---|---|
| What you install | One static binary, about 6 MB to download ([measured](#method)) | npm package, 27.5 MB unpacked across 299 files (`npm view claude-mem`), plus 27 plugin runtime dependencies, 24 of them tree-sitter grammars ([`plugin/package.json`](https://github.com/thedotmack/claude-mem/blob/v15.9.0/plugin/package.json)) |
| Runtime requirements | None. Go is needed only to build from source | Node.js 20+, Bun, uv ([README L318–321](https://github.com/thedotmack/claude-mem/blob/v15.9.0/README.md?plain=1#L318-L321)) |
| Background process | None. Learning runs as a short-lived detached worker after a session ends | Local HTTP worker service managed by Bun ([README L268](https://github.com/thedotmack/claude-mem/blob/v15.9.0/README.md?plain=1#L268)) |
| Claude Code hooks registered | 3 events: SessionStart, UserPromptSubmit, SessionEnd | 9 events, including PostToolUse on every tool and a synchronous PreToolUse before `Read`, `Bash`, `Grep` and `Glob` ([`plugin/hooks/hooks.json`](https://github.com/thedotmack/claude-mem/blob/v15.9.0/plugin/hooks/hooks.json)) |
| Model calls while you work | None in hooks or retrieval. A test enforces that the prompt-path packages cannot import an LLM SDK ([`hotpath_deps_test.go`](../internal/hook/hotpath_deps_test.go)) | An observer model compresses captured observations. After the hosted trial, "memory automatically falls back to your Anthropic plan" ([README L149](https://github.com/thedotmack/claude-mem/blob/v15.9.0/README.md?plain=1#L149)) |
| Cost control | Context pack capped at 700/1200 tokens. Learning has daily call and spend caps | Configurable provider and settings |
| Prompt hook latency | ~20 ms keyword-only, ~63 ms with local embeddings (p50, [measured](#method)) | Not measured here |
| Where memory lives | Markdown files in a git repo (`~/.culi/knowledge/`) that you can read, diff and edit | SQLite plus a Chroma vector database ([README L271](https://github.com/thedotmack/claude-mem/blob/v15.9.0/README.md?plain=1#L271)), with optional Cloud Sync to cmem.ai |
| Account or sign-in | Never | Installer offers a sign-in for its hosted observer. You can skip it with `--provider` or `CLAUDE_MEM_ONLINE_OPTIN=false` ([README L149–151](https://github.com/thedotmack/claude-mem/blob/v15.9.0/README.md?plain=1#L149-L151)) |
| What gets remembered | Only cards you wrote or approved | Everything captured, automatically |

## Memory benchmark

Does knowledge from one session reach the next ones? A small Go repo has four conventions that
cannot be inferred from its code: run tests only with `make verify`, wrap errors with
`inverr.Wrap`, store money as `int64` cents, start new files with an SPDX header. Session 1 asks
for a change and states the conventions. Sessions 2–5 ask for unrelated changes and never mention
them; each applicable convention is checked automatically from the session's commands and diff
(11 checks per run). Every arm runs in its own `HOME` with `claude -p`, Sonnet 5.5, and Claude
Code's built-in auto-memory disabled, except the arm that measures that feature alone.

| Arm (2026-11-04) | Conventions applied in sessions 2–5 | Avg. session cost | Tool's own model calls (5 sessions) |
|---|---|---|---|
| No memory | 2/11, 3/11 | $0.063 | 0 |
| Claude Code auto-memory only | 5/11, 4/11 | $0.067 | built in |
| claude-mem 15.9.0 | 9/11 (one valid run) | $0.090 | 29 observer turns, ~338k context tokens |
| culi v0.8.0 | 3/11 | $0.064 | 2 |
| culi with this change | 8/8 (session 5 lost to a quota error), 11/11 | $0.070 | 1–2 |

| Measured on the same machine | culi | claude-mem 15.9.0 |
|---|---|---|
| Install | 3–6 s, 16 MB | 83 s, 1.8 GB (1.2 GB without the npm cache) |
| `UserPromptSubmit` hook, p50 / p95 | 44 / 50 ms | 218 / 419 ms |
| Hook before every `Read`/`Bash`/`Grep`/`Glob` | none | 194 / 199 ms |

Read the numbers with their limits: one fixture, two runs per arm at most, and an agent that
varies run to run. claude-mem's second run is excluded because its observer paused itself at 96%
of the weekly Claude quota (a deliberate guard, not a failure), so it has one valid run. Its
observer also reads credentials only from the Claude Code credential store, so the sandbox needed
a `.credentials.json` holding the same token the session used.

## Where claude-mem is the better choice

- **Zero curation.** It captures everything without asking. culi's learner proposes cards and you
  approve them, which is deliberate but is work.
- **More agents.** claude-mem supports OpenCode, Cursor, T3 Code, OpenClaw, Hermes, Pi, DeepSeek
  Harness and others. culi supports Claude Code and Codex.
- **Semantic search out of the box.** culi uses SQLite FTS/BM25 by default and needs a local Ollama
  for semantic retrieval.
- **Session history.** claude-mem can recall what happened in a past session (timelines,
  observations). culi stores distilled knowledge, not session logs.
- **Team features and a large community.** It has a web viewer, a team workspace, and thousands of
  users reporting issues.

Pick culi if you want memory that costs no tokens or model calls while you work, stays as plain
files you control, and installs as one binary. Pick claude-mem if you want everything captured
automatically and use an agent culi does not support.

## Method

culi numbers were measured on 2026-11-03 on an Apple M1 Pro (macOS 27.0.1) with culi `d08f48b`
and a real store of 667 cards, in two modes: keyword-only (SQLite FTS/BM25, Ollama stopped) and
hybrid (BM25 plus one local `nomic-embed-text` embedding call through Ollama 0.32.1).

| Measurement | Result |
|---|---|
| Binary, `darwin/arm64`, stripped | 16.4 MB (6.3 MB as `.tar.gz`) |
| Binaries for linux/amd64, linux/arm64, darwin/amd64, windows/amd64 | 16.0–17.4 MB, all built with `CGO_ENABLED=0` from one machine |
| Prompt hook, keyword-only, fresh process per run, n=95 | p50 19.9 ms, p95 22.1 ms |
| Prompt hook, hybrid with local embeddings, 3 runs of n=95 | p50 62.7–63.8 ms, p95 70.6–78.4 ms |
| Context injected per prompt | keyword-only median ~285 tokens, hybrid median ~296 tokens, max ~616 tokens |
| Peak memory of one hook run | 22.5 MB RSS (`/usr/bin/time -l`) |

To reproduce:

```bash
export CULI_HOME=$(mktemp -d) CULI_NO_LEARN_SPAWN=1   # sandbox; leaves ~/.culi untouched
cp -R ~/.culi/knowledge "$CULI_HOME/" && culi index --full   # embeds cards when Ollama is up
python3 scripts/bench-hook.py "$(command -v culi)" --cwd ~/code/some-repo
sh scripts/dist.sh v0.0.0-local && ls -l dist/
```

claude-mem facts were read from the published `claude-mem@15.9.0` npm tarball
(`npm pack claude-mem@15.9.0 --ignore-scripts`) and its README at the `v15.9.0` tag. Corrections are
welcome; open an issue and this page will be updated.
