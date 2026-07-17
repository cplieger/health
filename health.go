// Package health implements healthchecks for distroless containers.
//
// Docker's HEALTHCHECK needs a command inside the container, and
// distroless images have no curl/wget/shell. This package covers the
// two shapes that problem takes:
//
//   - File marker (Marker, RunProbe): for containers whose main process
//     is your own Go binary. The running process touches the file at
//     DefaultPath at lifecycle points; the probe process (the same
//     binary re-invoked with a `health` subcommand) stats it. The app
//     owns the health decision via Set.
//   - HTTP probe (the nested module github.com/cplieger/health/probe):
//     for containers that wrap a third-party server which cannot
//     cooperate with a Marker but already exposes an HTTP endpoint
//     whose reachability IS the health signal. The standalone
//     probe/cmd/probe binary is installed into the image and wired as
//     the HEALTHCHECK.
//
// When you own the main process, prefer the file marker: Set expresses
// application state a network GET cannot. The rest of this doc comment
// describes the file-marker mode.
//
// Failure modes:
//   - If the marker directory is not writable (typically compose declares
//     `read_only: true` without a `tmpfs: /tmp` mount), the constructor
//     logs one Warn with a fix hint and enters degraded mode. In degraded
//     mode the long-running process treats Set / Cleanup as no-ops. The
//     probe process independently detects the same condition and reports
//     healthy, because the container is alive and the only broken piece
//     is the signaling channel. Reporting unhealthy would trigger a
//     Docker restart loop that cannot fix a compose misconfiguration.
//   - Transient failures during Set are logged at Warn but do not change
//     the marker's mode. A failed Set that leaves the marker absent on a
//     still-writable directory (e.g. directory churn) surfaces at the next
//     probe as unhealthy. A failure whose cause also leaves the directory
//     unwritable (full tmpfs), and a failed Set(false) that leaves the marker
//     present, are both reported healthy by the probe, matching the
//     degraded-mode rationale above.
//   - By default the probe checks existence only; staleness belongs to
//     Docker's --interval at the orchestrator level. Apps whose resident
//     loop refreshes the marker each cycle can opt into a freshness
//     deadline with WithMaxAge, under which a wedged loop (marker present
//     but old) probes unhealthy. See WithMaxAge for when not to arm it.
//
// Logging goes through slog.Default(); configure it via slog.SetDefault
// in main before constructing a Marker.
//
// Thread-safe; Set may be called from any goroutine.
package health

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Signal is the interface satisfied by *Marker. Consumers (e.g.
// HTTP handlers) can depend on this interface without importing the
// concrete type.
type Signal interface {
	Healthy() bool
}

// Compile-time assertion: *Marker satisfies Signal.
var _ Signal = (*Marker)(nil)

// DefaultPath is the default marker location. Docker healthchecks
// stat this path; the app creates and removes it at lifecycle points.
// /tmp is conventional because compose services with read_only:true
// typically mount /tmp as tmpfs.
const DefaultPath = "/tmp/.healthy"

// Marker implements the file-based distroless healthcheck pattern.
// Use NewMarker to construct it; call Set(bool) at lifecycle points;
// defer Cleanup on shutdown; call RunProbe from main when os.Args[1] is
// "health".
type Marker struct {
	path           string
	loggedFailSigs []string // failure signatures (msg + error) already logged during the current streak
	mu             sync.Mutex
	known          bool // true once Set has been called at least once
	healthy        bool // last value SUCCESSFULLY applied to the marker
	failed         bool // last filesystem op failed; gates duplicate warns
	degraded       bool // true when marker dir is not writable
}

// NewMarker constructs a marker for path and probes the parent
// directory for writability. On failure it logs a single Warn with a
// fix hint and returns a marker in degraded mode; callers need not
// branch on the result.
func NewMarker(path string) *Marker {
	m := &Marker{path: path}
	if err := probeHealthDir(path); err != nil {
		m.degraded = true
		slog.Warn("health marker directory not writable, "+
			"container will report healthy in degraded mode",
			"dir", filepath.Dir(path),
			"error", err,
			"hint", "compose.yaml with read_only:true requires "+
				"`tmpfs: [\"/tmp:size=1m,mode=1777,noexec,nosuid,nodev\"]`")
	}
	return m
}

// Set records the current liveness state and touches or removes the
// marker accordingly. Edge transitions (true↔false) are logged; repeated
// calls with the same value are silent. Safe to call from any goroutine.
// In degraded mode Set is a no-op. A filesystem failure is logged and
// swallowed; use SetChecked to observe it programmatically.
func (m *Marker) Set(ok bool) { _ = m.SetChecked(ok) }

// SetChecked is Set with the filesystem outcome reported: it returns nil
// when the marker now reflects ok, and the underlying error when the
// touch or remove failed (the same failure Set logs and swallows, so no
// extra log line is emitted). It exists for callers whose own success
// contract includes the marker write — e.g. a one-shot scan subcommand
// whose exit code an external scheduler alerts on, where a silently lost
// heartbeat should fail the invocation loudly instead. In degraded mode
// it returns nil: the marker channel is deliberately inert there (see
// the package doc's failure modes), and propagating an error would turn
// a compose misconfiguration into the restart or alert loop the degraded
// design exists to avoid.
func (m *Marker) SetChecked(ok bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.degraded {
		return nil
	}

	changed := !m.known || m.healthy != ok
	if msg, err := m.applyState(ok); err != nil {
		m.warnFailure(msg, err)
		return err
	}
	if recovered := m.recordState(ok); changed || recovered {
		logHealthState(ok)
	}
	return nil
}

// applyState performs the branch-specific filesystem operation for Set:
// touch the marker when ok, remove it (tolerating an already-absent file)
// otherwise. Returns the warn message and error on failure, or ("", nil)
// on success. Caller holds m.mu.
func (m *Marker) applyState(ok bool) (string, error) {
	if ok {
		if err := writeMarker(m.path); err != nil {
			return "failed to create health marker", err
		}
		return "", nil
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "failed to remove health marker", err
	}
	return "", nil
}

// logHealthState logs a health-state transition at the level matching the
// new state: Info when healthy, Warn when not.
func logHealthState(ok bool) {
	if ok {
		slog.Info("health state changed", "healthy", true)
		return
	}
	slog.Warn("health state changed", "healthy", false)
}

// Cleanup removes the marker. Typically called via defer at shutdown.
// In degraded mode Cleanup is a no-op.
func (m *Marker) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.degraded {
		return
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("failed to remove health marker on cleanup",
			"path", m.path, "error", err)
	}
}

// Healthy reports whether the marker file currently exists. Satisfies
// the Signal interface so HTTP handlers can report liveness without
// reaching into a package global. Strict os.Stat: a degraded marker
// directory (read-only mount, missing tmpfs) causes Healthy to return
// false so the HTTP endpoint honestly reports unhealthy.
//
// In degraded mode this intentionally diverges from ProbeCheck, which
// returns 0 (healthy) to avoid a Docker restart loop. Healthy returns
// false because HTTP consumers deserve an honest signal; see package doc.
func (m *Marker) Healthy() bool {
	if m == nil {
		return false
	}
	_, err := os.Stat(m.path)
	return err == nil
}

// ProbeOption configures the probe-side health decision (RunProbe and
// ProbeCheck). Without options the probe checks marker existence only.
type ProbeOption func(*probeConfig)

// probeConfig holds the probe-side knobs collected from ProbeOptions.
type probeConfig struct {
	maxAge time.Duration
}

// WithMaxAge arms an opt-in freshness deadline: a marker older than d
// is unhealthy (exit 1), turning the signal from a level ("the app
// last reported healthy") into a lease ("the app recently proved
// progress"). The writing side needs no new calls: every Set(true)
// refreshes the marker's mtime, so an app that already calls Set(true)
// once per work cycle gets heartbeat semantics by passing this option
// to RunProbe in its health subcommand.
//
// Arm it only where the resident process runs its own bounded work
// cycle at a known cadence, so a stale marker means a wedged loop that
// a restart fixes. Do NOT arm it for externally-triggered apps (a
// separate docker exec writes the marker): an idle resident between
// triggers is healthy, and restarting it cannot fix a trigger that
// stopped firing. Marker.Healthy (and therefore Handler) stays
// existence-based regardless of this option.
//
// A non-positive d disables the deadline (same as omitting the option).
func WithMaxAge(d time.Duration) ProbeOption {
	return func(c *probeConfig) { c.maxAge = d }
}

// RunProbe runs in the separate `health` subcommand process. It exits
// 0 if the marker is present (and fresh, when WithMaxAge is armed) or
// the marker directory is unwritable (degraded mode: the long-running
// process cannot signal through the filesystem, so the probe falls
// back to "alive"). It exits 1 when the marker is absent from a
// writable directory or stale past an armed deadline, which are the
// real unhealthy signals; the stderr diagnostic names the underlying
// stat failure when the cause is something other than absence.
func RunProbe(path string, opts ...ProbeOption) {
	code, reason := probeCheck(path, opts...)
	if code != 0 {
		fmt.Fprintln(os.Stderr, reason)
	}
	os.Exit(code)
}

// ProbeCheck implements the health-probe decision without calling
// os.Exit, so it can be unit-tested. Returns 0 for healthy or
// degraded, 1 for unhealthy.
func ProbeCheck(path string, opts ...ProbeOption) int {
	code, _ := probeCheck(path, opts...)
	return code
}

// probeCheck carries the shared probe decision plus the operator-facing
// diagnostic for the unhealthy exit: "marker absent" for the common
// ENOENT case, a stale-age line when an armed WithMaxAge deadline is
// exceeded, and the underlying stat error for anything else
// (permission, symlink loop, I/O), so RunProbe does not mislabel those
// as absence.
func probeCheck(path string, opts ...ProbeOption) (code int, reason string) {
	var cfg probeConfig
	for _, o := range opts {
		o(&cfg)
	}
	info, statErr := os.Stat(path) // #nosec G703 -- trusted caller-supplied marker path, existence check only
	if statErr == nil {
		if age := time.Since(info.ModTime()); cfg.maxAge > 0 && age > cfg.maxAge {
			return 1, fmt.Sprintf("unhealthy: marker stale: %s old exceeds max-age %s",
				age.Truncate(time.Second), cfg.maxAge)
		}
		return 0, ""
	}
	if err := probeHealthDir(path); err != nil {
		return 0, ""
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		return 1, "unhealthy: marker absent"
	}
	return 1, "unhealthy: marker stat failed: " + statErr.Error()
}

// --- helpers ---

// warnFailure logs a filesystem-op failure once per distinct (message,
// error) signature per streak, keying on both the static message AND the
// underlying error. A repeated identical failure stays silent (anti-spam),
// while a new message OR a new underlying error arising mid-streak still
// surfaces exactly once. This closes two facets of the coarser single-slot
// de-dup: alternating branch messages no longer re-spam within one streak,
// and a same-branch root-cause change (e.g. ENOSPC then EACCES) is no longer
// masked. Then it marks the marker failed. Caller holds m.mu.
func (m *Marker) warnFailure(msg string, err error) {
	if !m.failed {
		m.loggedFailSigs = m.loggedFailSigs[:0]
	}
	sig := msg + "\x00" + err.Error()
	if !slices.Contains(m.loggedFailSigs, sig) {
		slog.Warn(msg, "path", m.path, "error", err)
		m.loggedFailSigs = append(m.loggedFailSigs, sig)
	}
	m.failed = true
}

// recordState records a successfully applied liveness value and
// clears the failed flag, returning whether this call recovered
// from a prior failure streak. Caller holds m.mu.
func (m *Marker) recordState(ok bool) (recovered bool) {
	recovered = m.failed
	m.known, m.healthy, m.failed = true, ok, false
	m.loggedFailSigs = m.loggedFailSigs[:0]
	return recovered
}

// writeMarker atomically touches the marker file. A fresh os.Create is
// sufficient: the file is empty, and O_TRUNC on an existing file
// refreshes its mtime, which is the contract an armed WithMaxAge
// deadline reads. TestHealthMarker_SetTrue_refreshesMtime pins it.
func writeMarker(path string) error {
	f, err := os.Create(path) // #nosec G304 -- caller-supplied trusted path
	if err != nil {
		return err
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close: %w", closeErr)
	}
	return nil
}

// probeHealthDir verifies the marker's parent directory is writable by
// creating and deleting a temp file. Returns the underlying error on
// failure so callers can log with context.
func probeHealthDir(path string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".health-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	removed := false
	defer func() {
		if !removed {
			_ = os.Remove(name) // #nosec G703 -- name generated by os.CreateTemp above, not external input
		}
	}()

	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close probe: %w", closeErr)
	}
	if rmErr := os.Remove(name); rmErr != nil { // #nosec G703 -- name generated by os.CreateTemp above, not external input
		return fmt.Errorf("remove probe: %w", rmErr)
	}
	removed = true
	return nil
}
