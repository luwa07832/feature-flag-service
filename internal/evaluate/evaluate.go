// Package evaluate holds the environment-and-marker flag semantics shared by the
// real-time entry and the point-in-time history entry. Keeping the two call
// sites on one implementation guarantees identical results for the same
// environment, instant and marker.
package evaluate

import (
	"hash/fnv"
	"time"
)

// Definite and unevaluated outcomes. The strings are part of the public
// contract and are never logged or mutated.
const (
	StatusOn           = "on"
	StatusOff          = "off"
	StatusUnevaluated  = "unevaluated"
	StatusUnconfigured = "unconfigured"
)

// Config is the restorable view of one flag in one environment at one instant.
// WindowStart/WindowEnd are zero when the flag carries no effective window.
type Config struct {
	FlagID      string
	Enabled     bool
	RolloutPct  int
	WindowStart time.Time
	WindowEnd   time.Time
	HasWindow   bool
	VersionID   string
}

// MarkerValid reports whether marker matches the service marker format.
// Markers are 1–64 URL-safe characters: letters, digits, "_", "-" and ".",
// matching the constraint enforced when flag changes are recorded.
func MarkerValid(marker string) bool {
	if len(marker) < 1 || len(marker) > 64 {
		return false
	}
	for i := 0; i < len(marker); i++ {
		ch := marker[i]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '_' || ch == '-' || ch == '.':
		default:
			return false
		}
	}
	return true
}

// InWindow reports whether instant falls inside the effective window. A window
// is the half-open interval [start, end); a config without a window is always
// inside its window.
func InWindow(cfg Config, instant time.Time) bool {
	if !cfg.HasWindow {
		return true
	}
	if instant.Before(cfg.WindowStart) {
		return false
	}
	return instant.Before(cfg.WindowEnd)
}

// Bucket returns the deterministic 0–9999 bucket a marker lands in for an
// environment/flag pair. The hash input and modulus are fixed so repeated
// queries always agree.
func Bucket(environmentID, flagID, marker string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(environmentID))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(flagID))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(marker))
	return h.Sum32() % 10000
}

// Resolve evaluates a restored config at instant for marker. When the config is
// nil the flag had no effective configuration at the instant and the outcome is
// the definite "unconfigured" result. With an empty marker the configuration is
// returned untouched and the outcome is the uniform "unevaluated" result.
func Resolve(environmentID string, cfg *Config, instant time.Time, marker string) (enabled bool, status string) {
	if cfg == nil {
		return false, StatusUnconfigured
	}
	if marker == "" {
		return false, StatusUnevaluated
	}
	if !cfg.Enabled || !InWindow(*cfg, instant) {
		return false, StatusOff
	}
	cutoff := uint32(cfg.RolloutPct) * 100
	if Bucket(environmentID, cfg.FlagID, marker) < cutoff {
		return true, StatusOn
	}
	return false, StatusOff
}
