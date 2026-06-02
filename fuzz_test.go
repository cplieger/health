package health

import (
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

// FuzzProbeCheck fuzzes the ProbeCheck path-based decision logic with
// arbitrary file paths to ensure it never panics and returns only 0 or 1.
func FuzzProbeCheck(f *testing.F) {
	f.Add(".healthy")
	f.Add("/tmp/.healthy")
	f.Add("/nonexistent/path/.healthy")
	f.Add("")
	f.Add("../../../etc/passwd")

	dir := f.TempDir()

	f.Fuzz(func(t *testing.T, suffix string) {
		// Build a path rooted in the temp dir to avoid filesystem side effects.
		path := filepath.Join(dir, filepath.Base(suffix))
		// Randomly create/remove the file so ProbeCheck exercises both branches.
		if len(suffix)%2 == 0 {
			_ = os.WriteFile(path, nil, 0o600)
		} else {
			_ = os.Remove(path)
		}
		code := ProbeCheck(path)
		if code != 0 && code != 1 {
			t.Errorf("ProbeCheck(%q) = %d, want 0 or 1", path, code)
		}
	})
}
