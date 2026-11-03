---
title: Derive values during render instead of syncing state in useEffect
summary: 'Do not copy props into state with an effect; compute derived values while rendering.'
triggers:
    keywords:
        - useEffect
        - useState
        - React
---

If a value can be computed from props or other state, compute it during render (with useMemo if it is expensive).
Copying props into state inside useEffect causes an extra render and stale values.
