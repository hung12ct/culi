---
title: Fan out independent I/O with errgroup and a limit
summary: 'errgroup.WithContext plus SetLimit runs N calls in parallel and cancels on the first error.'
triggers:
    keywords:
        - errgroup
        - parallel
        - concurrency limit
---

Use `g, ctx := errgroup.WithContext(ctx)` and `g.SetLimit(n)` to run independent requests concurrently with a bound.
The first error cancels the rest; collect results by index to keep order.
