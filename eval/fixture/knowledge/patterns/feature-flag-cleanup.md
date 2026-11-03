---
title: Remove feature flags after full rollout
summary: 'Once a flag is at 100% and stable, delete it and the dead branch.'
triggers:
    keywords:
        - feature flag
        - rollout
---

A flag at 100% for a week is tech debt: remove the flag check, the old code path, and the flag definition.
Track every flag with an owner and an expiry date.
