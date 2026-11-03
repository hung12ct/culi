---
title: Never commit secrets
summary: 'API keys and passwords come from environment variables or a secrets manager.'
triggers:
    keywords:
        - api key
        - secret
        - .env
---

Credentials such as API keys, tokens and database passwords are read from the environment or a secrets manager at runtime.
Add `.env` to .gitignore and rotate anything that was ever committed.
