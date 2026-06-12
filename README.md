# health

[![CI](https://github.com/cplieger/health/actions/workflows/ci.yaml/badge.svg)](https://github.com/cplieger/health/actions/workflows/ci.yaml)
[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/health.svg)](https://pkg.go.dev/github.com/cplieger/health)
[![Go Report Card](https://goreportcard.com/badge/github.com/cplieger/health)](https://goreportcard.com/report/github.com/cplieger/health)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/health/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/health)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

> File-based healthcheck for distroless containers

A standalone Go library implementing the file-marker health-signal pattern for Docker containers that lack a shell. The running process touches/removes a marker file; the probe process (re-invoked binary) stats it. Handles degraded mode (read-only filesystem) gracefully. Standard library only (test dependency: pgregory.net/rapid).

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

## API

- `DefaultPath` — default marker path (`/tmp/.healthy`)
- `Signal` — interface with `Healthy() bool`
- `Marker` — main type; implements `Signal`
- `NewMarker(path string, opts ...Option) *Marker` — constructor (probes dir writability)
- `(*Marker).Set(ok bool)` — touch or remove marker
- `(*Marker).Cleanup()` — remove marker on shutdown
- `(*Marker).Healthy() bool` — stat-based liveness check
- `Status` — JSON response struct emitted by `Handler` (fields: `Status`, `Timestamp`)
- `Handler(s Signal) http.Handler` — optional JSON health endpoint
- `RunProbe(path string)` — probe process entry (calls os.Exit)
- `ProbeCheck(path string) int` — testable probe logic (0=healthy, 1=unhealthy)
- `ProbeDir(path string) error` — reports whether the marker's parent directory is writable (the degraded-mode check NewMarker/ProbeCheck use internally, exported for consumers and their tests)

## Unsupported by design

The following features are deliberately excluded. This library complements
HTTP-based health libraries (e.g. hellofresh/health-go, alexliesenfeld/health)
rather than competing with them.

| Feature | Rationale |
|---------|-----------|
| Registered dependency checks | `Set(bool)` is the aggregation point; the app owns the decision logic. A check registry is a fundamentally different abstraction (~150 LOC, specialized). |
| Liveness/readiness split | Docker Compose has one HEALTHCHECK. For K8s, create two `Marker` instances with different paths. |
| Graceful shutdown / context.Context | `Cleanup()` is the shutdown action. No background goroutines exist to cancel. |
| Status-change callbacks | State transitions are logged via slog. Wrap `Set()` for custom callbacks. |
| Marker staleness / mtime checks | Docker's `--interval`/`--timeout` handle staleness at the orchestrator level. |
| Prometheus metrics | Trivially added by consumers: `prometheus.NewGaugeFunc(opts, func() float64 { ... })`. |
| Custom marker content | The pattern's elegance is `os.Stat` — no parsing, no format versioning. |

## License

GPL-3.0 — see [LICENSE](LICENSE).
