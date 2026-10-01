// Package timeutil defines the single time semantics every public entry shares.
//
// Timestamps are instants on the UTC timeline. Inputs are accepted in RFC 3339
// form (with either seconds or fractional seconds, e.g. "2026-03-01T08:00:00Z"
// or "2026-03-01T16:00:00+08:00") and are rendered back in the canonical form
// "2006-01-02T15:04:05.999999999Z07:00" normalised to UTC with a trailing "Z".
package timeutil

import (
	"errors"
	"time"
)

// Layouts accepted at every entry that takes a timestamp.
var layouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

// Parse interprets raw with the service-wide time semantics. It rejects values
// that do not match any accepted layout; callers map the error to
// InvalidTimestamp.
func Parse(raw string) (time.Time, error) {
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, errors.Join(errInvalidTimestamp, lastErr)
}

// Format renders instant in the canonical service form.
func Format(instant time.Time) string {
	return instant.UTC().Format(time.RFC3339Nano)
}

// errInvalidTimestamp is sentinel-free on purpose: callers decide the public
// code. Parse returns a non-nil error whenever the input is unusable.
var errInvalidTimestamp = errors.New("invalid timestamp")
