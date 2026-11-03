---
title: Wrap Go errors with a package prefix
summary: 'Return fmt.Errorf("pkg: doing x: %w", err) so callers can unwrap and logs show the origin.'
triggers:
    keywords:
        - fmt.Errorf
        - '%w'
        - error wrapping
---

Wrap every returned error with the package name and the operation: `fmt.Errorf("store: loading card %s: %w", id, err)`.
Keep messages lowercase without trailing punctuation, and use `%w` so `errors.Is` still works.
