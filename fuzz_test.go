package health

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// FuzzHandlerSignal fuzzes the HTTP handler with arbitrary signal states
// and request methods/paths. This exercises the JSON render path and
// ensures no panics regardless of input combinations.
func FuzzHandlerSignal(f *testing.F) {
	f.Add(true, "GET", "/health")
	f.Add(false, "GET", "/health")
	f.Add(true, "POST", "/healthz")
	f.Add(false, "HEAD", "/")

	f.Fuzz(func(t *testing.T, healthy bool, method, target string) {
		if method == "" {
			method = "GET"
		}
		if target == "" {
			target = "/"
		}
		h := Handler(stubSignal{healthy: healthy})
		req, err := http.NewRequest(method, target, nil)
		if err != nil {
			t.Skip() // invalid method/target can't construct a request; not a handler concern
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if healthy && rec.Code != http.StatusOK {
			t.Errorf("healthy signal: got %d, want 200", rec.Code)
		}
		if !healthy && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("unhealthy signal: got %d, want 503", rec.Code)
		}
	})
}

// FuzzProbeCheck fuzzes the ProbeCheck path-based decision logic. It
// asserts two invariants: (1) bounded output -- ProbeCheck never panics
// and returns only 0 or 1 for ANY raw input path; (2) state consistency
// -- in a controlled writable dir, a present marker yields 0 and an absent
// marker yields 1. The marker name is a hash of the input so pathological
// inputs ("..", ".", "/") can neither escape the temp dir nor collapse onto it.
func FuzzProbeCheck(f *testing.F) {
	f.Add(".healthy")
	f.Add("/tmp/.healthy")
	f.Add("/nonexistent/path/.healthy")
	f.Add("")
	f.Add(".")
	f.Add("/")
	f.Add("..")
	f.Add("../")
	f.Add("../../../etc/passwd")

	dir := f.TempDir()

	f.Fuzz(func(t *testing.T, suffix string) {
		// Invariant 1 (bounded output): ProbeCheck must never panic and
		// must return only 0 or 1 for any raw input path.
		if got := ProbeCheck(suffix); got != 0 && got != 1 {
			t.Fatalf("ProbeCheck(%q) = %d, want 0 or 1", suffix, got)
		}

		// Invariant 2 (state consistency in a controlled writable dir):
		// derive a filesystem-safe, collision-free child name so
		// pathological inputs cannot escape dir or collapse onto it.
		name := fmt.Sprintf("m-%x", sha256.Sum256([]byte(suffix)))
		path := filepath.Join(dir, name)

		if len(suffix)%2 == 0 {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Skip()
			}
			if got := ProbeCheck(path); got != 0 {
				t.Errorf("ProbeCheck(%q) with marker present = %d, want 0", path, got)
			}
			_ = os.Remove(path) // keep the shared TempDir bounded across fuzz execs
			return
		}
		_ = os.Remove(path)
		if got := ProbeCheck(path); got != 1 {
			t.Errorf("ProbeCheck(%q) with marker absent in writable dir = %d, want 1", path, got)
		}
	})
}
