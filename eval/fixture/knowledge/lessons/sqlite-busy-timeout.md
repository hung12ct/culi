---
title: Enable WAL and busy_timeout for concurrent SQLite writers
summary: 'Two processes writing one SQLite file hit SQLITE_BUSY unless WAL and a busy timeout are set.'
triggers:
    keywords:
        - sqlite
        - SQLITE_BUSY
        - WAL
---

Concurrent writers on one SQLite database fail with `database is locked` by default.
Open with `PRAGMA journal_mode=WAL` and `PRAGMA busy_timeout=5000`, and keep write transactions short.
