package health

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pgregory.net/rapid"
)

// TestHealthMarker_SetCreatesAndRemoves covers the happy path: a writable
// dir, Set(true) creates the marker, Set(false) removes it.
func TestHealthMarker_SetCreatesAndRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	m.Set(true)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker should exist after Set(true): %v", err)
	}

	m.Set(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should not exist after Set(false): %v", err)
	}
}

// TestHealthMarker_Cleanup confirms Cleanup removes the marker and is
// safe to call when the marker already does not exist.
func TestHealthMarker_Cleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	m.Set(true)
	m.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should be gone after Cleanup: %v", err)
	}

	// Second cleanup must not error.
	m.Cleanup()
}

// TestHealthMarker_DegradedMode verifies that when the marker directory
// is not writable, the marker enters degraded mode: Set and Cleanup are
// no-ops and no file is ever created.
func TestHealthMarker_DegradedMode(t *testing.T) {
	// Create a read-only directory to simulate a compose misconfiguration.
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}

	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)

	if !m.degraded {
		// Some environments (root, permissive filesystems like Windows
		// or containers) allow writes through 0500; skip rather than
		// fail in those cases.
		t.Skip("test environment bypasses directory mode; skipping")
	}

	m.Set(true)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("degraded marker should never create file: %v", err)
	}
	m.Cleanup() // must not panic
}

// TestHealthMarker_Idempotent ensures repeated Set(true) and Set(false)
// calls are safe and converge to the expected file state.
func TestHealthMarker_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	for range 3 {
		m.Set(true)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker should exist after repeated Set(true): %v", err)
	}

	for range 3 {
		m.Set(false)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should not exist after repeated Set(false): %v", err)
	}
}

// TestHealthMarker_Property exercises arbitrary Set sequences and asserts
// that the file state always matches the last Set argument.
func TestHealthMarker_Property(t *testing.T) {
	dir := t.TempDir()
	rapid.Check(t, func(rt *rapid.T) {
		// A fresh subdir per iteration so markers from earlier iterations
		// don't leak into later ones.
		nonce := rapid.StringMatching(`[a-z0-9]{8}`).Draw(rt, "nonce")
		subdir := filepath.Join(dir, nonce)
		if err := os.Mkdir(subdir, 0o755); err != nil {
			rt.Fatalf("mkdir subdir: %v", err)
		}
		path := filepath.Join(subdir, ".healthy")
		m := NewMarker(path)

		calls := rapid.SliceOfN(rapid.Bool(), 1, 30).Draw(rt, "calls")
		for _, ok := range calls {
			m.Set(ok)
		}
		last := calls[len(calls)-1]

		_, err := os.Stat(path)
		exists := err == nil
		if exists != last {
			rt.Fatalf("after Set(%v): exists=%v, want %v",
				last, exists, last)
		}
	})
}

// TestProbeHealthDir_Writable confirms the probe succeeds on a normal
// writable temp dir and leaves no artifact behind.
func TestProbeHealthDir_Writable(t *testing.T) {
	dir := t.TempDir()
	if err := probeHealthDir(filepath.Join(dir, ".healthy")); err != nil {
		t.Fatalf("probeHealthDir on writable dir: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left artifacts behind: %v", entries)
	}
}

// TestProbeHealthDir_NonExistent confirms a missing parent directory is
// reported as an error rather than masked.
func TestProbeHealthDir_NonExistent(t *testing.T) {
	err := probeHealthDir(filepath.Join(t.TempDir(), "nope", ".healthy"))
	if err == nil {
		t.Fatal("expected error for non-existent parent dir")
	}
}

func TestProbeCheck_healthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	if got := ProbeCheck(path); got != 0 {
		t.Errorf("ProbeCheck(marker present) = %d, want 0", got)
	}
}

func TestProbeCheck_unhealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if got := ProbeCheck(path); got != 1 {
		t.Errorf("ProbeCheck(marker absent, writable dir) = %d, want 1", got)
	}
}

func TestProbeCheck_degraded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, ".healthy")
	if err := probeHealthDir(path); err == nil {
		t.Skip("test environment bypasses directory mode; skipping")
	}
	if got := ProbeCheck(path); got != 0 {
		t.Errorf("ProbeCheck(unwritable dir) = %d, want 0 (degraded)", got)
	}
}

// TestHealthMarker_Healthy covers the Healthy() method so consumers can
// use *Marker via the Signal interface. The method checks
// marker-file presence via strict os.Stat.
func TestHealthMarker_Healthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	// Before any Set, the file does not exist — Healthy is false.
	if m.Healthy() {
		t.Error("Healthy() = true before Set(true), want false")
	}

	m.Set(true)
	if !m.Healthy() {
		t.Error("Healthy() = false after Set(true), want true")
	}

	m.Set(false)
	if m.Healthy() {
		t.Error("Healthy() = true after Set(false), want false")
	}

	m.Set(true)
	m.Cleanup()
	if m.Healthy() {
		t.Error("Healthy() = true after Cleanup(), want false")
	}
}

// TestProbeDir_Writable confirms the exported ProbeDir succeeds on a
// writable dir and leaves no artifact behind (mirrors the internal
// probeHealthDir test, via the public wrapper consumers use).
func TestProbeDir_Writable(t *testing.T) {
	dir := t.TempDir()
	if err := ProbeDir(filepath.Join(dir, ".healthy")); err != nil {
		t.Fatalf("ProbeDir on writable dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ProbeDir left artifacts behind: %v", entries)
	}
}

// TestProbeDir_NonExistent confirms a missing parent directory is
// reported as an error rather than masked.
func TestProbeDir_NonExistent(t *testing.T) {
	if err := ProbeDir(filepath.Join(t.TempDir(), "nope", ".healthy")); err == nil {
		t.Fatal("expected error for non-existent parent dir")
	}
}

// TestHealthMarker_HealthyDegraded verifies that in degraded mode
// Healthy reports false (strict os.Stat semantics).
func TestHealthMarker_HealthyDegraded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}

	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)

	if !m.degraded {
		t.Skip("test environment bypasses directory mode; skipping")
	}

	if m.Healthy() {
		t.Error("Healthy() = true in degraded mode, want false (strict os.Stat)")
	}
}

// TestHealthMarker_ConcurrentSetCleanupHealthy exercises concurrent
// Set, Cleanup, and Healthy calls under the race detector.
func TestHealthMarker_ConcurrentSetCleanupHealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			m.Set(i%2 == 0)
		}()
		go func() {
			defer wg.Done()
			m.Cleanup()
		}()
		go func() {
			defer wg.Done()
			_ = m.Healthy()
		}()
	}
	wg.Wait()
}

func TestHealthMarker_SetWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}

	m := NewMarker(path)
	if m.degraded {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	m.Set(true) // must not panic when os.Create fails

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat(%q) after failed Set(true): %v", path, err)
	}
	if !info.IsDir() {
		t.Errorf("Set(true) on unwritable marker path created a file; want path unchanged (dir)")
	}
}

func TestHealthMarker_SetRemoveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	m := NewMarker(path)
	if m.degraded {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	m.Set(false) // must not panic when os.Remove fails with non-ErrNotExist

	if _, err := os.Stat(path); err != nil {
		t.Errorf("path should still exist after failed Set(false) removal: %v", err)
	}
}

// TestHealthMarker_SetWriteFailureWarnDedupAndRecovery verifies the
// failure-gating contract: under a persistent write failure Set emits
// exactly one Warn (not one per call), and once the write finally
// succeeds it logs a recovery "health state changed" Info even though
// the requested value never changed.
func TestHealthMarker_SetWriteFailureWarnDedupAndRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}

	m := NewMarker(path)
	if m.degraded {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Three failing calls (marker path is a directory -> os.Create fails).
	m.Set(true)
	m.Set(true)
	m.Set(true)

	if got := strings.Count(buf.String(), "failed to create health marker"); got != 1 {
		t.Errorf("want exactly 1 write-failure Warn under persistent failure, got %d\nlog:\n%s", got, buf.String())
	}

	// Remove the blocking directory so the write can succeed, then recover.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove blocking dir: %v", err)
	}
	buf.Reset()
	m.Set(true)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker should exist after recovery Set(true): %v", err)
	}
	if !strings.Contains(buf.String(), "health state changed") {
		t.Errorf("want recovery 'health state changed' Info after write succeeds; log:\n%s", buf.String())
	}
}

// TestHealthMarker_SetRemoveFailureWarnDedup verifies the remove path
// emits exactly one Warn under a persistent remove failure.
func TestHealthMarker_SetRemoveFailureWarnDedup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	m := NewMarker(path)
	if m.degraded {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Non-empty directory -> os.Remove fails with non-ErrNotExist.
	m.Set(false)
	m.Set(false)
	m.Set(false)

	if got := strings.Count(buf.String(), "failed to remove health marker"); got != 1 {
		t.Errorf("want exactly 1 remove-failure Warn under persistent failure, got %d\nlog:\n%s", got, buf.String())
	}
}

func TestHealthMarker_CleanupRemoveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	m := NewMarker(path)
	if m.degraded {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	m.Cleanup() // must not panic when os.Remove fails with non-ErrNotExist

	if _, err := os.Stat(path); err != nil {
		t.Errorf("path should still exist after failed Cleanup removal: %v", err)
	}
}

func TestRunProbe_exits(t *testing.T) {
	if os.Getenv("HEALTH_RUNPROBE_CASE") != "" {
		RunProbe(os.Getenv("HEALTH_RUNPROBE_PATH"))
		return
	}

	okPath := filepath.Join(t.TempDir(), ".healthy")
	if err := os.WriteFile(okPath, nil, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), ".healthy")

	tests := []struct {
		name     string
		path     string
		wantExit int
	}{
		{"marker_present_exits_0", okPath, 0},
		{"marker_absent_writable_dir_exits_1", badPath, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestRunProbe_exits")
			cmd.Env = append(os.Environ(),
				"HEALTH_RUNPROBE_CASE=1",
				"HEALTH_RUNPROBE_PATH="+tt.path)
			err := cmd.Run()

			if tt.wantExit == 0 {
				if err != nil {
					t.Errorf("RunProbe(%q) exited non-zero: %v", tt.path, err)
				}
				return
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("RunProbe(%q): expected *exec.ExitError, got %v", tt.path, err)
			}
			if ee.ExitCode() != tt.wantExit {
				t.Errorf("RunProbe(%q) exit = %d, want %d", tt.path, ee.ExitCode(), tt.wantExit)
			}
		})
	}
}

// TestHealthMarker_Healthy_nilReceiver pins the documented nil-receiver
// contract: a nil *Marker reports unhealthy rather than panicking, both
// directly and when handed through the Signal interface (a non-nil
// interface wrapping a nil pointer).
func TestHealthMarker_Healthy_nilReceiver(t *testing.T) {
	var m *Marker

	if m.Healthy() {
		t.Error("(*Marker)(nil).Healthy() = true, want false")
	}

	var s Signal = m
	if s.Healthy() {
		t.Error("Signal backed by nil *Marker: Healthy() = true, want false")
	}
}
