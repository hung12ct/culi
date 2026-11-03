# Retrieval evaluation

`culi eval` runs labeled prompts through the same gate → retrieve → pack funnel the prompt hook
uses, then scores the injected cards against the expected ones. It answers the question a memory
tool has to answer: when you type a prompt, does the right knowledge arrive, and does nothing
arrive when nothing applies?

```bash
make eval                             # bundled fixture suite in a throwaway CULI_HOME
make eval EVAL_FLAGS=--keyword-only   # same, without Ollama
culi eval --suite my-suite.yaml       # your own suite against your own store
```

## Metrics

| Metric | Meaning |
|---|---|
| Hit rate | Share of positive cases where at least one expected card was injected |
| MRR | Mean reciprocal rank of the first expected card (1.0 = always first) |
| Recall | Share of expected cards injected, averaged over positive cases |
| Precision | Share of injected cards that were expected. Strict: a useful but unlabeled card counts as a miss |
| Abstain accuracy | Share of abstain cases (empty `expect`) where nothing was injected |

`--min-hit 0.9` makes the command exit non-zero below that hit rate. The Go test
`TestFixtureSuite` runs the fixture suite keyword-only on every `make test` and in CI, so a ranking
or gating change that loses a case fails before it ships.

## Bundled suite

[`eval/suite.yaml`](../eval/suite.yaml) has 38 cases against the 36 cards in
[`eval/fixture/knowledge`](../eval/fixture/knowledge): 32 developer prompts phrased in a
developer's words rather than the card's, and 6 abstain prompts (small talk, translation, trivia).
Several cards are near neighbours on purpose (timeouts vs. retries, SQLite vs. Postgres locking).
The maintainer wrote both the cards and the prompts, and the suite is small. Read its numbers as a
regression baseline, not as a general accuracy claim.

Results on 2026-11-03, Apple M1 Pro, default config (700-token budget):

| Mode | Hit rate | MRR | Recall | Precision | Abstain | Tokens per case | Latency p50 / p95 |
|---|---|---|---|---|---|---|---|
| Keyword (SQLite FTS/BM25) | 91% (29/32) | 0.89 | 87% | 64% | 100% (6/6) | 69 | 0.6 ms / 1.6 ms |
| Hybrid (+ `nomic-embed-text` via Ollama) | 94% (30/32) | 0.92 | 90% | 65% | 100% (6/6) | 70 | 25 ms / 30 ms |

Latency here is in-process with the store already open. The end-to-end hook, including process
start, is measured in [comparison.md](comparison.md#method).

### Known misses

The suite keeps its failing cases; they are the useful part.

- **`webhook-503`** ("should try again when the partner API returns 503"): nothing injected.
  Trigger keywords shorter than 4 characters are ignored, so `503` never pins the retry card, and
  only one other term overlaps.
- **`add-column`** ("add a nullable column to the accounts table"): nothing injected. The lexical
  floor counts matches in a card's title, summary, aliases and keywords, not its body, and "column"
  appears only in the body.
- **`flag-at-100`** (keyword mode only): the wrong card wins on shared vocabulary. Hybrid
  retrieval fixes it.

Precision near 65% means about one injected card in three is not one the suite expected. Some of
those are reasonable neighbours; others are noise the budget still pays for.
