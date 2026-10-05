# Contributing to health

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- A change to what degraded mode reports updates the package comment, the `CheckHealthy`, `RunProbe`, `ProbeCheck` and `Handler` comments, the README's degraded-mode section and `docs/file-marker.md` together. A missed one tells callers the opposite of what the code does.

## Checks

- `probe/` is a separate Go module, so `go test ./...` at the repository root skips its tests. After a change there, also run `go test -race ./...` inside `probe/`, which CI tests as its own module.
- After a change to degraded mode, run the tests as a non-root user on Linux. Root can write into the `0500` directory those tests create, so they skip and `go test` still reports a pass.

## Releases

`probe/` has its own `probe/vX.Y.Z` tags. Shipped files under `probe/` release the probe module, and shipped files elsewhere release the root module.

Split a change to both into one commit per module, or both releases get the same commit type and notes.
