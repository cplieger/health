package health_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// ExampleProbeHTTP shows the HTTP liveness probe for containers that
// wrap a third-party server exposing an HTTP endpoint.
func ExampleProbeHTTP() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := health.ProbeHTTP(context.Background(), srv.URL)
	fmt.Println("healthy:", err == nil)
	// Output:
	// healthy: true
}

// ExampleHTTPProbeCheck shows the multi-URL probe behind cmd/probe: all
// URLs must answer 2xx within one shared timeout.
func ExampleHTTPProbeCheck() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	code := health.HTTPProbeCheck(os.Stderr, health.DefaultHTTPProbeTimeout, srv.URL)
	fmt.Println("exit code:", code)
	// Output:
	// exit code: 0
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
