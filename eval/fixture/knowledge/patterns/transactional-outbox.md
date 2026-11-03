---
title: Publish events through a transactional outbox
summary: 'Write the event to an outbox table in the same transaction as the state change, then relay it.'
triggers:
    keywords:
        - outbox
        - kafka
        - event publishing
---

Publishing to a broker after committing can lose events if the process dies in between.
Insert the event into an outbox table in the same transaction, and have a relay publish and mark it sent.
