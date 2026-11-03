---
title: omitempty drops false, 0 and empty strings
summary: 'A bool field with omitempty disappears from JSON when it is false.'
triggers:
    keywords:
        - omitempty
        - encoding/json
---

`json:",omitempty"` omits zero values, so `false`, `0` and `""` vanish from responses.
Use a pointer (`*bool`) or drop omitempty when clients must see the zero value.
