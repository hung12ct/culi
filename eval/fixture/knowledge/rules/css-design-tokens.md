---
title: Use design tokens instead of hard-coded colors
summary: 'Colors, spacing and radii come from CSS custom properties, not literals.'
triggers:
    keywords:
        - css variables
        - theme
        - dark mode
---

Reference `var(--color-primary)` and friends instead of hex literals so themes and dark mode work.
Add a new token rather than a one-off value.
