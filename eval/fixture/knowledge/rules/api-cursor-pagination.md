---
title: Paginate list endpoints with opaque cursors
summary: 'List endpoints return a page plus a next_cursor; never the full table.'
triggers:
    keywords:
        - pagination
        - next_cursor
        - limit
---

Every list endpoint accepts `limit` (with a max) and `cursor`, and returns `next_cursor`.
Cursors encode the last sort key, so pages stay stable while rows are inserted, unlike OFFSET.
