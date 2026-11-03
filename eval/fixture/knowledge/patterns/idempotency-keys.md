---
title: Make POST endpoints idempotent with idempotency keys
summary: 'Clients send an Idempotency-Key header; the server stores and replays the first response.'
triggers:
    keywords:
        - idempotency
        - duplicate charge
---

Accept an `Idempotency-Key` header on mutating endpoints, store the key with the response, and replay that response for retries.
This prevents duplicate orders or charges when clients retry after a timeout.
