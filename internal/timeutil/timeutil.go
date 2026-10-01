// Package timeutil defines the service's unified time semantics: every
// timestamp entering or leaving the service is an RFC 3339 string with an
// explicit offset (or Z). It is interpreted to an absolute Unix-nanosecond
// instant, so history-point selection and time-window checks are independent
// of the server's local zone.
package timeutil

import (
	"errors"
	"time"
)

// ErrInvalidTimestamp reports a timestamp the service cannot interpret.
var ErrInvalidTimestamp = errors.New("invalid timestamp")

// Parse interprets at as an absolute RFC 3339 (including fractional seconds)
// instant. Ambiguous and unparseable inputs are rejected.
func Parse(at string) (int64, error) {
	if at == "" {
		return 0, ErrInvalidTimestamp
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return 0, ErrInvalidTimestamp
	}
	return parsed.UnixNano(), nil
}

// Format renders a Unix-nanosecond instant back to canonical RFC 3339 UTC.
func Format(nanos int64) string {
	return time.Unix(0, nanos).UTC().Format(time.RFC3339Nano)
}
