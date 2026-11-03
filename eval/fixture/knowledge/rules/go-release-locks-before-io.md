---
title: Release mutexes before network or disk calls
summary: 'Copy what you need under the lock, unlock, then do the slow call.'
triggers:
    keywords:
        - sync.Mutex
        - RWMutex
        - lock contention
---

Holding a mutex across an HTTP call, a query or a file write serializes every caller behind the slowest request.
Copy the needed fields under the lock, release it, then perform the I/O.
