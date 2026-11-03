---
title: Push rewritten branches with --force-with-lease
summary: 'After a rebase, --force-with-lease refuses to overwrite commits you have not seen.'
triggers:
    keywords:
        - rebase
        - force push
---

After rebasing a shared branch, push with `git push --force-with-lease` instead of `--force`.
It fails if someone else pushed in the meantime, instead of silently deleting their commits.
