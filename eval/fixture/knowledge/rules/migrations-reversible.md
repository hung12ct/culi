---
title: Ship a down migration with every schema change
summary: 'Each migration has an up and a tested down step.'
triggers:
    keywords:
        - migration
        - schema change
---

Every schema migration comes with a down step that restores the previous schema, and both are run in CI.
Adding a column: add it nullable first, backfill, then add constraints in a later migration.
