---
title: Create Postgres indexes CONCURRENTLY on large tables
summary: 'A plain CREATE INDEX locks writes for the whole build.'
triggers:
    keywords:
        - postgres
        - CREATE INDEX
        - lock
---

On a large production table, `CREATE INDEX` blocks inserts and updates until it finishes.
Use `CREATE INDEX CONCURRENTLY` (outside a transaction) and check for an INVALID index if it fails.
