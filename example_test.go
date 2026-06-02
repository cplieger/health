package health_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/health"
)

// Example demonstrates the two-process healthcheck pattern.
// The long-running process creates a marker; the probe process stats it.
func Example() {
	path := filepath.Join(os.TempDir(), ".healthy-example")
	m := health.NewMarker(path)
	defer m.Cleanup()

	m.Set(true)
	fmt.Println("healthy:", m.Healthy())

	m.Set(false)
	fmt.Println("healthy:", m.Healthy())
	// Output:
	// healthy: true
	// healthy: false
}

// ExampleProbeCheck shows how to use ProbeCheck for a testable probe
// that does not call os.Exit.
func ExampleProbeCheck() {
	dir, _ := os.MkdirTemp("", "health-example-*")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, ".healthy")

	// No marker yet — writable dir means unhealthy.
	fmt.Println("code:", health.ProbeCheck(path))

	// Create marker — healthy.
	os.WriteFile(path, nil, 0o600)
	fmt.Println("code:", health.ProbeCheck(path))
	// Output:
	// code: 1
	// code: 0
}
