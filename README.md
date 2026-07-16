# health

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/health.svg)](https://pkg.go.dev/github.com/cplieger/health)
[![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/health)](https://github.com/cplieger/health/blob/main/go.mod)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/health/badges/coverage.json)](https://github.com/cplieger/health/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/health/badges/mutation.json)](https://github.com/cplieger/health/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13212/badge)](https://www.bestpractices.dev/projects/13212)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/health/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/health)

> Healthchecks for distroless containers: file marker + HTTP probe

A standalone Go library for Docker healthchecks in containers that lack a shell. Two modes:

- **File marker** — for containers whose main process is your own Go binary. The running process touches/removes a marker file; the probe process (re-invoked binary) stats it. Handles degraded mode (read-only filesystem) gracefully.
- **HTTP probe** — for containers wrapping a third-party server (Caddy, an upstream daemon) that cannot cooperate with a marker but already exposes an HTTP endpoint whose reachability is the health signal. `cmd/probe` is the ready-made static binary to bake into the image.

When you own the main process, prefer the file marker: `Set(bool)` expresses application state a network GET cannot. Standard library only (test dependency: pgregory.net/rapid).

## Install

Go: `go get github.com/cplieger/health@latest`

## Usage

### Main process

```go
package main

import "github.com/cplieger/health"

func main() {
    m := health.NewMarker(health.DefaultPath)
    defer m.Cleanup()

    // Mark healthy once ready
    m.Set(true)

    // ... run application ...
}
```

### Health subcommand (probe process)

```go
if len(os.Args) > 1 && os.Args[1] == "health" {
    health.RunProbe(health.DefaultPath)
}
```

> **External triggers and file ownership:** the marker belongs to whoever
> created it. If a separate `docker exec` process updates it (a job scheduler
> invoking your binary's `run`/`sync` subcommand), run that exec as the same
> UID as the container's main process, e.g. `user = 568:568` in an Ofelia
> job-exec block. A mismatched exec user fails the marker write with
> permission denied, and only the health signal is lost, silently.

### HTTP probe (wrapped third-party servers)

For images whose main process is not your code — so nothing can touch a
marker — bake the standalone probe binary into the image and point it at
the endpoint(s) that define liveness:

```dockerfile
FROM golang:1.26-alpine AS probe
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/cplieger/health/cmd/probe@latest

FROM gcr.io/distroless/static-debian12
COPY --from=probe /out/probe /probe
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/probe", "http://127.0.0.1:2019/config/"]
```

Multiple URLs probe multiple surfaces in one run (all must answer 2xx
within one shared `-timeout` budget, default 5s):

```dockerfile
CMD ["/probe", "http://127.0.0.1:80/health", "http://127.0.0.1:2019/config/"]
```

Exit codes: 0 all healthy, 1 any probe failed (each failure written to
stderr, visible in `docker inspect`), 2 usage error. From Go, the same
logic is `health.ProbeHTTP(ctx, url)` / `health.HTTPProbeCheck(w,
timeout, urls...)` / `health.RunHTTPProbe(timeout, urls...)`.

### Optional HTTP handler (K8s HTTP probes)

For containers that also expose an HTTP endpoint, the library provides an
optional `Handler` that emits JSON status — compatible with K8s HTTP liveness
probes and mirroring the response shape of hellofresh/health-go:

```go
import "github.com/cplieger/health"

m := health.NewMarker(health.DefaultPath)
http.Handle("/healthz", health.Handler(m))
```

Response (200 OK):

```json
{"status":"OK","timestamp":"2025-01-01T00:00:00Z"}
```

Response (503 Service Unavailable):

```json
{"status":"Unavailable","timestamp":"2025-01-01T00:00:00Z"}
```

> **Degraded mode caveat:** when the marker directory is unwritable (e.g.
> `read_only: true` with no `/tmp` tmpfs), `Handler` reports 503, intentionally
> diverging from the `health` subcommand probe (`ProbeCheck`), which reports
> healthy to avoid a Docker restart loop. Do not wire `Handler` as the _sole_
> liveness probe on a service that may run read-only without a `/tmp` tmpfs, or
> it will restart-loop a container that is actually alive.

## API

- `DefaultPath` — default marker path (`/tmp/.healthy`)
- `Signal` — interface with `Healthy() bool`
- `Marker` — main type; implements `Signal`
- `NewMarker(path string) *Marker` — constructor (probes dir writability)
- `(*Marker).Set(ok bool)` — touch or remove marker
- `(*Marker).Cleanup()` — remove marker on shutdown
- `(*Marker).Healthy() bool` — stat-based liveness check
- `Status` — JSON response struct emitted by `Handler` (fields: `Status`, `Timestamp`)
- `Handler(s Signal) http.Handler` — optional JSON health endpoint
- `RunProbe(path string)` — probe process entry (calls os.Exit)
- `ProbeCheck(path string) int` — testable probe logic (0=healthy, 1=unhealthy)
- `DefaultHTTPProbeTimeout` — default shared budget for one HTTP probe run (5s)
- `ProbeHTTP(ctx context.Context, url string) error` — single HTTP liveness GET; nil on a 2xx final response
- `HTTPProbeCheck(w io.Writer, timeout time.Duration, urls ...string) int` — testable multi-URL probe (0=all healthy, 1 otherwise; probes all URLs, one failure line each; zero URLs is unhealthy)
- `RunHTTPProbe(timeout time.Duration, urls ...string)` — HTTP probe process entry (calls os.Exit); `cmd/probe` is the ready-made binary around it

## Unsupported by design

The following features are deliberately excluded. This library complements
HTTP-based health libraries (e.g. hellofresh/health-go, alexliesenfeld/health)
rather than competing with them — those are server-side check frameworks,
while this library's HTTP probe is a client-side liveness GET for the
HEALTHCHECK side of the same connection.

| Feature                             | Rationale                                                                                                                                                 |
| ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Registered dependency checks        | `Set(bool)` is the aggregation point; the app owns the decision logic. A check registry is a fundamentally different abstraction (~150 LOC, specialized). |
| Liveness/readiness split            | Docker Compose has one HEALTHCHECK. For K8s, create two `Marker` instances with different paths.                                                          |
| Graceful shutdown / context.Context | `Cleanup()` is the shutdown action. No background goroutines exist to cancel.                                                                             |
| Status-change callbacks             | State transitions are logged via slog. Wrap `Set()` for custom callbacks.                                                                                 |
| Marker staleness / mtime checks     | Docker's `--interval`/`--timeout` handle staleness at the orchestrator level.                                                                             |
| Prometheus metrics                  | Trivially added by consumers: `prometheus.NewGaugeFunc(opts, func() float64 { ... })`.                                                                    |
| Custom marker content               | The pattern's elegance is `os.Stat` — no parsing, no format versioning.                                                                                   |

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude Opus](https://www.anthropic.com/claude) and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0 — see [LICENSE](LICENSE).
