---
title: Use npm ci in CI
summary: 'npm install can drift from package-lock.json; npm ci installs exactly the lockfile.'
triggers:
    keywords:
        - npm ci
        - package-lock
---

CI runs `npm ci`, which installs exactly what package-lock.json pins and fails if it is out of date.
`npm install` may resolve newer versions and produce builds that differ from local ones.
