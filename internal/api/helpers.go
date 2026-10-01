package api

import (
	"regexp"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// KeyPattern is the published identifier grammar for environments and flags.
var KeyPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ErrorCode is the vocabulary of the single top-level error object every
// entry returns.
const (
	codeInvalidRequest      = "InvalidRequest"
	codeEnvironmentNotFound = "EnvironmentNotFound"
	codeFlagNotFound        = "FlagNotFound"
	codeAlreadyExists       = "AlreadyExists"
	codeInvalidTimestamp    = "InvalidTimestamp"
	codeInvalidMarker       = "InvalidMarker"
	codeStorageUnavailable  = "storage_unavailable"
)

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// windowJSON renders a time window; nil timestamps stay null.
func windowJSON(startsAt, endsAt *int64) gin.H {
	var starts, ends any
	if startsAt != nil {
		starts = timeutil.Format(*startsAt)
	}
	if endsAt != nil {
		ends = timeutil.Format(*endsAt)
	}
	return gin.H{"starts_at": starts, "ends_at": ends}
}

func configJSON(r store.ConfigRecord) gin.H {
	return gin.H{
		"flag_key":    r.FlagKey,
		"environment": r.Environment,
		"version":     r.Version,
		"enabled":     r.Enabled,
		"percentage":  r.Percentage,
		"window":      windowJSON(r.StartsAt, r.EndsAt),
		"changed_at":  timeutil.Format(r.ChangedAt),
		"tombstone":   r.Tombstone,
	}
}
