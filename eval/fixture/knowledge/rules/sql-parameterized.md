---
title: Always parameterize SQL queries
summary: 'Pass user input as bind parameters, never by string concatenation.'
triggers:
    keywords:
        - sql injection
        - placeholder
---

Build queries with placeholders (`WHERE name = ?` or `$1`) and pass values separately.
Concatenating request input into SQL is an injection vulnerability even for internal tools.
