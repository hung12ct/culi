<!--
Title must follow Conventional Commits, e.g. `feat(serve): add reconcile screen`
or `fix(hook): fail open on retrieval timeout`.
-->

## What & why

<!-- One or two sentences: what this changes and the problem it solves. Link the issue. -->

Closes #

## Changes

<!-- Bullet the notable changes. One logical change per PR — split unrelated work. -->

-

## Testing

<!-- How you verified this. Name the tests you added/ran. Goroutine/mutex code must pass under -race. -->

-

## Checklist

- [ ] Title is a Conventional Commit (`feat:`, `fix:`, `docs:`, `refactor:`, `chore:`, …)
- [ ] `make check` is clean (fmt + vet + lint + build + test)
- [ ] `make test-race` passes for any change touching goroutines
- [ ] Errors wrapped with a package prefix (`fmt.Errorf("<pkg>: ...: %w", err)`)
- [ ] Hot-path code (hook/retrieve/pack/store/knowledge/session/embed) stays stdlib + modernc + yaml only, fails open, and respects the latency ceiling
- [ ] Token-budget and "never destroy user content" contracts upheld (see CLAUDE.md)
