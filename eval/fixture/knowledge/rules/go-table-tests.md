---
title: Write table-driven tests with t.Run subtests
summary: 'One slice of cases, one loop, a named subtest per case.'
triggers:
    keywords:
        - table-driven
        - t.Run
---

Express test cases as a slice of structs with a name, inputs and expected output, and run each with `t.Run(tc.name, ...)`.
Add a case for every boundary: zero, empty, negative, maximum.
