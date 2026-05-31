# health
> File-based healthcheck for distroless containers

A standalone Go library implementing the file-marker health-signal pattern for Docker containers that lack a shell. The running process touches/removes a marker file; the probe process (re-invoked binary) stats it. Handles degraded mode (read-only filesystem) gracefully. Standard library only (test dependency: pgregory.net/rapid).

## Install
<!-- TODO: registry/pull link -->
Go: `go get github.com/cplieger/health@latest`

## Usage
```go
package main

import "github.com/cplieger/health"

func main() {
    m := health.NewMarker(health.DefaultPath)
    defer m.Cleanup()

    // On startup
    m.Set(true)

    // In a health subcommand
    // health.RunProbe(health.DefaultPath)
}
```

## API
- `DefaultPath` — default marker path (`/tmp/.healthy`)
- `Signal` — interface with `Healthy() bool`
- `Marker` — main type; implements `Signal`
- `NewMarker(path string) *Marker` — constructor (probes dir writability)
- `(*Marker).Set(ok bool)` — touch or remove marker
- `(*Marker).Cleanup()` — remove marker on shutdown
- `(*Marker).Healthy() bool` — stat-based liveness check
- `RunProbe(path string)` — probe process entry (calls os.Exit)
- `ProbeCheck(path string) int` — testable probe logic (0=healthy, 1=unhealthy)

## License
GPL-3.0 — see [LICENSE](LICENSE).
