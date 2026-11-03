---
title: Stage specific files when committing
summary: 'Never run git add -A or git add . — list the files that belong to the change.'
triggers:
    keywords:
        - git add
        - staging
---

Stage only the files that belong to the logical change with `git add path/one path/two`.
`git add -A` sweeps in scratch files, secrets and unrelated edits.
