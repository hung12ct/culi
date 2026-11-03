---
title: Use structured logging with slog
summary: 'Log key-value pairs through log/slog instead of fmt.Println.'
triggers:
    keywords:
        - slog
        - logging
---

Use `log/slog` with key-value attributes: `slog.Info("order paid", "order_id", id)`.
No `fmt.Println` or `log.Printf` in services; logs must be machine-parseable.
