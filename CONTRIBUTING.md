# Contributing to health

Notes on the file-marker pattern, public API, and the design contract that
keep this library small. Most of the guidance here is about preserving the
deliberate non-goals and the degraded-mode behavior when you change code.

## The two-role pattern

`health` is a standard-library-only Go package (one test-only dependency,
`pgregory.net/rapid`) that signals container liveness through a single
marker file instead of a shell, `curl`, or an open port. Distroless images
have nothing to run inside a Docker `HEALTHCHECK`, so the binary plays two
roles against the same file at `DefaultPath` (`/tmp/.healthy`):

- **Main process** — owns a `*Marker`, calls `Set(true)` once ready,
  `Set(false)` when not, and `defer m.Cleanup()` on shutdown. `Set` touches
  or removes the marker file.
- **Probe process** — the same binary re-invoked with a `health`
  subcommand. It stats the marker and exits 0 (healthy) or 1 (unhealthy)
  via `RunProbe`.

There is no shared state beyond the file itself, and no background
goroutines. `Marker` is safe to call from any goroutine (guarded by a
`sync.Mutex`); `Set`/`Cleanup` log edge transitions through
`slog.Default()`, so configure logging with `slog.SetDefault` in `main`
before constructing a `Marker`.

## The HTTP probe (second mode)

The file marker assumes the container's main process is your own binary.
Images that repackage a third-party server (Caddy, an upstream daemon)
have nothing that can call `Set`, but they do expose an HTTP endpoint
whose reachability is the health signal. The nested module
`github.com/cplieger/health/probe` (the `probe/` directory, its own
release lane with `probe/vX.Y.Z` tags) covers that case: `probe.URL(ctx,
url)` is a single liveness GET (2xx after redirects = healthy),
`probe.Check(w, timeout, urls...)` is the testable multi-URL aggregate
(probes ALL URLs within one shared budget, one failure line each; zero
URLs is deliberately unhealthy), and `probe.Run` is the `os.Exit`
wrapper. `probe/cmd/probe` is the ready-made static binary an image
bakes in as its `HEALTHCHECK` (exit 0/1, 2 on usage errors).

The decision rule between the modes: own the main process → file marker
(`Set` expresses application state a GET cannot); wrap a third-party
server → HTTP probe. The probe is a client; the "complements, not
competes" stance toward server-side check frameworks (hellofresh,
alexliesenfeld) is unchanged. Everything stays standard-library only.

Wiring in a consuming app looks like this:

```go
func main() {
    if len(os.Args) > 1 && os.Args[1] == "health" {
        health.RunProbe(health.DefaultPath) // calls os.Exit
    }

    m := health.NewMarker(health.DefaultPath)
    defer m.Cleanup()
    m.Set(true)
    // ... run application ...
}
```

## Degraded mode is load-bearing — mind the divergence

`NewMarker` probes the marker's parent directory for writability at
construction (via the internal `probeHealthDir` writability probe). When the
directory is not writable — typically a compose service with
`read_only: true` and no `tmpfs: /tmp` mount — the marker enters
**degraded mode**: it logs one `Warn` with a fix hint, and `Set`/`Cleanup`
become no-ops. Callers never branch on the result.

The subtle part is that the two readers intentionally disagree in degraded
mode, and both behaviors are deliberate:

- `ProbeCheck` (and therefore `RunProbe`) returns **0 / healthy** when the
  directory is unwritable. The container is alive; the only broken piece is
  the signaling channel. Reporting unhealthy would trigger a Docker restart
  loop that cannot fix a compose misconfiguration.
- `Marker.Healthy()` (the `Signal` method the HTTP `Handler` calls) does a
  strict `os.Stat` and returns **false** in degraded mode, because HTTP
  consumers deserve an honest signal.

If you find yourself "fixing" this divergence so the two agree, stop — it
is the design. The reasoning lives in the package doc comment and the
`Healthy` / `ProbeCheck` doc comments; update those together if the
behavior ever legitimately changes.

## Unsupported by design — a binding contract

The "[Unsupported by design](README.md#unsupported-by-design)" table in
`README.md` lists deliberate non-features, not a TODO list. This library
complements HTTP-based health libraries (hellofresh/health-go,
alexliesenfeld/health) rather than competing with them.

A PR that adds one of those non-goals will be declined regardless of
quality. If you think a non-goal should change, open an issue first.

## Public API

The whole surface is small enough to enumerate; keep it that way.

- `DefaultPath` — default marker path (`/tmp/.healthy`).
- `Signal` — interface with `Healthy() bool`; `*Marker` satisfies it (a
  compile-time assertion guards this).
- `Marker` — main type. `NewMarker(path) *Marker`, `Set(ok bool)`,
  `Cleanup()`, `Healthy() bool`.
- `RunProbe(path string)` — probe-process entry point; calls `os.Exit`.
- `ProbeCheck(path string) int` — the same decision without `os.Exit`, so
  it is unit-testable (0 = healthy or degraded, 1 = unhealthy).
- `Handler(s Signal) http.Handler` — optional JSON endpoint for K8s HTTP
  probes; 200 `{"status":"OK",...}` when healthy, 503
  `{"status":"Unavailable",...}` otherwise. A nil `Signal` always reports 503.
- `Status` — the JSON response struct (`Status`, `Timestamp`) emitted by
  `Handler`.
In the `github.com/cplieger/health/probe` nested module:

- `probe.DefaultTimeout` — default shared budget for one HTTP probe run
  (5s, matching the BusyBox-wget recipes the probe replaces).
- `probe.URL(ctx, url) error` — single HTTP liveness GET (2xx = nil).
- `probe.Check(w, timeout, urls...) int` — testable multi-URL probe
  (0 = all healthy, 1 otherwise; probes all URLs; zero URLs is unhealthy).
- `probe.Run(timeout, urls...)` — probe-process entry point; calls
  `os.Exit`. `probe/cmd/probe` is the flag-parsing binary around it.

## Local development

The module targets the Go version pinned in `go.mod`. Use that
toolchain or newer.

```sh
go build ./...
go test ./...
go test -race ./...
```

The concurrent `Set`/`Cleanup`/`Healthy` test is the main reason to run
with `-race` before pushing. Benchmarks (`ProbeCheck`, `Healthy`, the
handler render path, `Set`) live in `bench_test.go`:

```sh
go test -bench=. -benchmem .
```

### Linting and formatting

Lint config is `.golangci.yaml` (golangci-lint v2). It enables `gosec`,
`gocritic`, `revive`, `gocyclo` (complexity cap 15), `sloglint` (kv-only),
and others. Formatting is `gofumpt` with `extra-rules` plus `gci` import
grouping (standard → third-party); `golangci-lint run` reports unformatted
files as issues, so format before pushing.

```sh
golangci-lint run
golangci-lint fmt
```

Note `sloglint` is kv-only: log with key/value pairs
(`slog.Warn("msg", "key", val)`), matching the existing `Set`/`NewMarker`
call sites.

### Fuzzing

Fuzz targets live in `health_fuzz_test.go` and `handler_fuzz_test.go`. Run
one at a time with a time budget:

```sh
go test -run='^$' -fuzz=FuzzHandlerSignal -fuzztime=30s .
go test -run='^$' -fuzz=FuzzProbeCheck -fuzztime=30s .
```

New path-handling or HTTP-render logic should come with a fuzz target or an
added seed corpus entry.

### Mutation testing

`.gremlins.yaml` configures [Gremlins](https://gremlins.dev) mutation
testing (synced from `cplieger/ci`; change it upstream). Note that
`health.go` is on the central `exclude-files` list (the filesystem ops
produce stuck live mutants without integration tests), so the score
reflects `handler.go` and the probe logic. Run it locally to check that new
tests kill mutants:

```sh
gremlins unleash .
```

## Test layout

Tests live beside the code (standard Go layout), split by intent — match
the right file when adding cases:

- `health_test.go` — marker lifecycle, degraded mode, idempotency, the
  `rapid` property test (arbitrary `Set` sequences converge to the last
  value), probe-dir checks, and the concurrent race test.
- `handler_test.go` — HTTP handler status codes and JSON shape (defines the
  shared `stubSignal`).
- `httpprobe_test.go` — HTTP probe status table, the `rapid` status-boundary
  property (exactly [200,299] is healthy), redirect/timeout/refused paths,
  and the multi-URL aggregate (probes-all, names-failing-URL, zero-URLs).
- `handler_fuzz_test.go` — `FuzzHandlerSignal`; `health_fuzz_test.go` —
  `FuzzProbeCheck`.
- `example_test.go` — runnable `Example` / `ExampleProbeCheck` functions
  that double as documentation; keep their `// Output:` blocks correct.
- `bench_test.go` — allocation/throughput benchmarks.

The degraded-mode tests `t.Skip` when the environment bypasses directory
mode (root, or permissive filesystems like Windows), so a green run on such
a host does not exercise that path — verify degraded-mode changes on Linux.

## Commits and PRs

Branch from `main`, keep changes focused with tests, and open a PR. This
account uses [Conventional Commits](https://www.conventionalcommits.org/)
parsed by git-cliff (`cliff.toml`) to build release notes, so the commit
type drives the version bump: `feat:`, `fix:`, `sec:`, and
`chore:`/`docs:`/`refactor:`/`test:` (no release). Write the subject as the
changelog line a consumer would read.

## Conduct & security

By participating you agree to the org-wide
[Code of Conduct](https://github.com/cplieger/.github/blob/main/CODE_OF_CONDUCT.md).
Report security issues through the
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md) —
never in a public issue.
