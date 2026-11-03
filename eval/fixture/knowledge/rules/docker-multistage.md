---
title: Build containers with multi-stage Dockerfiles
summary: 'Compile in a builder stage and copy only the binary into a distroless or scratch image.'
triggers:
    keywords:
        - Dockerfile
        - image size
---

Use a builder stage with the full toolchain and a final stage from `gcr.io/distroless/static` or `scratch` that copies only the binary.
This cuts images from gigabytes to tens of megabytes and removes the shell attack surface.
