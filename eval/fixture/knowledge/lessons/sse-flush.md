---
title: Flush after each server-sent event
summary: 'Without Flush and X-Accel-Buffering: no, proxies deliver an SSE stream all at once.'
triggers:
    keywords:
        - SSE
        - server-sent events
        - nginx
---

Call `flusher.Flush()` after writing each event, set `Content-Type: text/event-stream`, and send `X-Accel-Buffering: no` so nginx does not buffer.
Otherwise clients receive every event in one burst when the handler returns.
