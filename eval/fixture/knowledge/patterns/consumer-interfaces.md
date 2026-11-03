---
title: Define small interfaces where they are consumed
summary: 'Accept a 2-3 method interface declared by the caller, not a large provider interface.'
triggers:
    keywords:
        - interface
        - mock
---

Declare the interface in the package that uses it, with only the methods it calls.
Tests then stub two methods instead of mocking a twenty-method provider type.
