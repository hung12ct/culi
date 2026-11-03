---
title: bufio.Scanner fails on lines longer than 64 KiB
summary: "Scanner returns 'token too long'; use bufio.Reader.ReadBytes or raise Scanner.Buffer."
triggers:
    keywords:
        - bufio.Scanner
        - token too long
        - jsonl
---

`bufio.Scanner` stops with `bufio.Scanner: token too long` when a line exceeds 64 KiB, which JSONL logs often do.
Read with `bufio.NewReader(f).ReadBytes('\n')` or call `sc.Buffer(buf, maxSize)` before scanning.
