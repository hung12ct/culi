# Culi product vision

Culi is the local context control plane for coding agents: one trusted knowledge system that
works across harnesses, delivers only what is relevant, and makes every learning decision
inspectable and reversible.

## The product loop

1. **Capture** — import explicit guidance and observe useful session outcomes.
2. **Curate** — propose small lessons; the user approves what becomes durable knowledge.
3. **Deliver** — route the smallest relevant context to Claude, Codex, and future harnesses.
4. **Explain** — show what was sent, why it matched, what it cost, and how to correct it.

## Console principles

- **Action before analytics.** Lead with health and the next useful decision; keep token metrics secondary.
- **Progressive disclosure.** A new user should understand Home and Review without knowing cards, hooks, or granularity.
- **One honest source of truth.** Never present a browser-only mutation as saved; destructive changes stay explicit and reversible.
- **Harness-neutral language.** Describe agents and sessions by default, then identify Claude or Codex where provenance matters.
- **Local confidence.** Make local-only operation, spend limits, failures, and degraded modes visible without creating alarm fatigue.

## Product direction

### Now — trustworthy daily control

- Health and next actions on Home.
- Fast evidence-first review of proposed lessons.
- Searchable knowledge maintenance and per-session delivery traces.
- Responsive, keyboard-accessible operation.

### Next — guided setup and diagnosis

- A first-run checklist for harness hooks, MCP, learning backend, and history backfill.
- Harness health inside the console using the same facts as `culi doctor`.
- Clear learning backlog controls: scan, run, retry, and inspect failures.

### Later — quality intelligence

- Explain why a card matched and compare useful versus noisy delivery.
- Detect overlap, contradiction, and stale knowledge before it reaches agents.
- Measure outcomes by harness and repository without splitting the canonical store.

