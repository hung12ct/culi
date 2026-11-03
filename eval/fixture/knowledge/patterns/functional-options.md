---
title: Configure constructors with functional options
summary: 'NewX(required, ...Option) instead of long parameter lists or config structs full of zero values.'
triggers:
    keywords:
        - functional options
        - constructor
---

Constructors take required arguments positionally and everything optional as `...Option`, where `type Option func(*X)`.
Defaults live in the constructor, so adding an option never breaks callers.
