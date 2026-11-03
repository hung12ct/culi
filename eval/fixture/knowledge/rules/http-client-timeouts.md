---
title: Set timeouts on every outbound HTTP client
summary: 'http.DefaultClient has no timeout; a stalled upstream blocks forever.'
triggers:
    keywords:
        - http.Client
        - timeout
---

Never use `http.DefaultClient` for outbound calls: it has no timeout, so a stalled peer hangs the goroutine forever.
Create `&http.Client{Timeout: 10 * time.Second}` and also pass a request context with a deadline.
