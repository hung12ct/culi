---
title: Retry transient failures with exponential backoff and jitter
summary: 'Retry 429/5xx and network errors a bounded number of times, with growing randomized delays.'
triggers:
    keywords:
        - retry
        - backoff
        - 503
---

Retry only transient failures (timeouts, connection resets, 429, 502, 503, 504) and cap the attempts.
Double the delay each attempt and add random jitter so clients do not retry in lockstep. Honor Retry-After.
