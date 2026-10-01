// Package eval holds the deterministic, side-effect free rules for evaluating
// feature flags: gradual rollout bucketing, time-window gating and marker
// validation. Real-time evaluation and historical point-in-time evaluation
// share this package so both surfaces always agree.
package eval

import (
	"errors"
	"hash/fnv"
	"regexp"
)

// Status is the single vocabulary every evaluation surface returns.
type Status string

const (
	// StatusOn means the flag is enabled for the marker at the queried time.
	StatusOn Status = "on"
	// StatusOff means a configuration exists but the marker is not served yet.
	StatusOff Status = "off"
	// StatusUnconfigured means no effective configuration existed at the time;
	// it is a definitive status, never a fallback to later configuration.
	StatusUnconfigured Status = "unconfigured"
	// StatusNotEvaluated is the unified status used when no marker is supplied:
	// the response carries a configuration snapshot only.
	StatusNotEvaluated Status = "not_evaluated"
)

// MarkerPattern is the published marker grammar: 1-64 lowercase letters,
// digits, underscores or hyphens. The same grammar is enforced by the
// real-time evaluator.
var MarkerPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ErrInvalidMarker reports a marker that does not match MarkerPattern.
var ErrInvalidMarker = errors.New("invalid marker")

// ValidateMarker reports whether marker uses the published marker format.
// An empty marker means "no marker supplied" and is accepted here; callers
// decide between snapshot-only and evaluated responses.
func ValidateMarker(marker string) error {
	if marker == "" || MarkerPattern.MatchString(marker) {
		return nil
	}
	return ErrInvalidMarker
}

// Window is the effective time window of a flag configuration. A nil bound
// means that side is unbounded. Times are Unix nanoseconds.
type Window struct {
	StartsAt *int64
	EndsAt   *int64
}

// ActiveAt reports whether the window covers at. A window is active when
// start <= at < end, matching the existing evaluation semantics.
func (w Window) ActiveAt(at int64) bool {
	if w.StartsAt != nil && at < *w.StartsAt {
		return false
	}
	if w.EndsAt != nil && at >= *w.EndsAt {
		return false
	}
	return true
}

// Rollout is the gradual-rollout input: the flag key, the environment the
// percentage applies to and the percentage in [0,100].
type Rollout struct {
	FlagKey     string
	Environment string
	Percentage  int
}

// Served reports whether marker belongs to the rollout. Bucketing is a pure
// function of (flag, environment, marker), so identical inputs always produce
// identical results on every service instance.
func (r Rollout) Served(marker string) bool {
	if r.Percentage <= 0 {
		return false
	}
	if r.Percentage >= 100 {
		return true
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(r.FlagKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(r.Environment))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(marker))
	bucket := int(h.Sum64() % 100)
	return bucket < r.Percentage
}

// Configuration is a restored flag configuration as it existed at one point
// in time. A nil pointer means the flag had no effective configuration then.
type Configuration struct {
	FlagKey    string
	Enabled    bool
	Percentage int
	Window     Window
}

// Evaluated resolves one flag for one marker at at. A nil configuration yields
// StatusUnconfigured rather than reusing any current or later configuration.
func Evaluated(environment string, cfg *Configuration, marker string, at int64) Status {
	if cfg == nil {
		return StatusUnconfigured
	}
	if !cfg.Enabled || !cfg.Window.ActiveAt(at) {
		return StatusOff
	}
	rollout := Rollout{FlagKey: cfg.FlagKey, Environment: environment, Percentage: cfg.Percentage}
	if !rollout.Served(marker) {
		return StatusOff
	}
	return StatusOn
}
