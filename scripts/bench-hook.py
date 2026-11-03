#!/usr/bin/env python3
"""Measure end-to-end latency and injected size of `culi hook user-prompt-submit`.

Each run is a fresh process (the way Claude Code and Codex invoke the hook) and
uses a session id unique to this invocation, so the per-session novelty gate
never skips a prompt because an earlier benchmark run already sent it.

Run it against a sandbox copy of a store so the real activity log stays clean:

    export CULI_HOME=$(mktemp -d) CULI_NO_LEARN_SPAWN=1
    cp -R ~/.culi/knowledge "$CULI_HOME/" && culi index --full
    python3 scripts/bench-hook.py "$(command -v culi)" --cwd ~/code/some-repo

Token counts are estimated as chars / 4.
"""

import argparse
import json
import statistics
import subprocess
import time
import uuid

PROMPTS = [
    "add a retry with exponential backoff to the http tool and wrap errors with package prefix",
    "write a table-driven test for the session fork helper",
    "why does the sse stream drop events when the client reconnects",
    "refactor the main loop into smaller methods",
    "bump the changelog and cut a new release tag",
]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("binary")
    ap.add_argument("--cwd", required=True, help="repository the prompts pretend to come from")
    ap.add_argument("--runs", type=int, default=100)
    ap.add_argument("--warmup", type=int, default=5)
    args = ap.parse_args()

    run_id = uuid.uuid4().hex[:8]
    times, sizes = [], []
    for i in range(args.runs):
        payload = json.dumps({
            "session_id": f"bench-{run_id}-{i}",
            "cwd": args.cwd,
            "prompt": PROMPTS[i % len(PROMPTS)],
            "hook_event_name": "UserPromptSubmit",
        }).encode()
        start = time.perf_counter()
        out = subprocess.run([args.binary, "hook", "user-prompt-submit"],
                             input=payload, capture_output=True, check=True).stdout
        times.append((time.perf_counter() - start) * 1000)
        ctx = json.loads(out)["hookSpecificOutput"]["additionalContext"] if out.strip() else ""
        sizes.append(len(ctx))

    times = sorted(times[args.warmup:])
    p95 = times[int(len(times) * 0.95) - 1]
    print(f"latency  n={len(times)} p50={statistics.median(times):.1f}ms "
          f"p95={p95:.1f}ms max={times[-1]:.1f}ms")
    med = statistics.median(sizes)
    print(f"injected median={med:.0f} chars (~{med / 4:.0f} tokens) "
          f"max={max(sizes)} chars (~{max(sizes) / 4:.0f} tokens) empty={sizes.count(0)}")


if __name__ == "__main__":
    main()
