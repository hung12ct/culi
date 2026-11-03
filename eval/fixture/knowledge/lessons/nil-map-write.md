---
title: Initialize maps before writing to them
summary: "Writing to a nil map panics with 'assignment to entry in nil map'."
triggers:
    keywords:
        - nil map
        - panic
---

A declared but unmade map is nil: reads work, writes panic with `assignment to entry in nil map`.
Create it with `make(map[K]V)` or in the constructor, especially for struct fields.
