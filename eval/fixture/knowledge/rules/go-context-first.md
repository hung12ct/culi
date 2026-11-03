---
title: Pass ctx as the first parameter on I/O functions
summary: 'Any function that does network, disk or database work takes context.Context first.'
triggers:
    keywords:
        - context.Context
        - ctx
---

Functions that perform I/O take `ctx context.Context` as their first parameter and pass it down to every call.
Never store a context in a struct; never use context.Background() below main.
