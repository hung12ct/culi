---
title: Stop time.Ticker values to avoid leaks
summary: 'A ticker that is never stopped keeps its goroutine and channel alive.'
triggers:
    keywords:
        - time.Ticker
        - goroutine leak
---

Every `time.NewTicker` needs a matching `defer t.Stop()`, and the loop must exit on `ctx.Done()`.
Unstopped tickers inside per-request goroutines show up as steadily growing goroutine counts in pprof.
