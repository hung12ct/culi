---
title: Use the standard testing package, not testify
summary: 'Assert with if-statements and t.Fatalf; no assertion libraries.'
triggers:
    keywords:
        - testify
        - assert
---

Tests use only the standard `testing` package. Write `if got != want { t.Fatalf("got %v, want %v", got, want) }`.
Stubs are small structs implementing the interface under test.
