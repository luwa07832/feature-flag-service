package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/eval"
	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// explain serves the single-flag decision explanation entry. It is read-only:
// no configuration history is ever written. Validation order is fixed so
// every malformed request gets a unique result: path keys, at, marker,
// environment, flag. Without at the service's current time is used; with at
// the last configuration version not later than at is restored.
func explain(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, environment, flagKey) {
			return
		}
		at := st.Now()
		if raw, provided := c.GetQuery("at"); provided {
			parsed, err := timeutil.Parse(raw)
			if err != nil {
				fail(c, http.StatusBadRequest, codeInvalidTimestamp, "at must be an RFC 3339 timestamp")
				return
			}
			at = parsed
		}
		marker := c.Query("marker")
		if marker == "" {
			fail(c, http.StatusBadRequest, codeInvalidMarker, "marker query parameter is required")
			return
		}
		if err := eval.ValidateMarker(marker); err != nil {
			fail(c, http.StatusBadRequest, codeInvalidMarker, "marker must match [a-z0-9_-]{1,64}")
			return
		}
		envExists, err := st.EnvironmentExists(environment)
		if err != nil {
			internalFailure(c)
			return
		}
		if !envExists {
			fail(c, http.StatusNotFound, codeEnvironmentNotFound, "the environment does not exist")
			return
		}
		flagExists, err := st.FlagExists(flagKey)
		if err != nil {
			internalFailure(c)
			return
		}
		if !flagExists {
			fail(c, http.StatusNotFound, codeFlagNotFound, "the flag does not exist")
			return
		}
		record, err := st.EffectiveConfigAt(flagKey, environment, at)
		if err != nil {
			internalFailure(c)
			return
		}
		var cfg *eval.Configuration
		if record != nil && !record.Tombstone {
			cfg = &eval.Configuration{
				FlagKey:    record.FlagKey,
				Enabled:    record.Enabled,
				Percentage: record.Percentage,
				Window:     eval.Window{StartsAt: record.StartsAt, EndsAt: record.EndsAt},
			}
		}
		status, reason := eval.Explained(environment, cfg, marker, at)
		c.JSON(http.StatusOK, gin.H{
			"environment":  environment,
			"flag_key":     flagKey,
			"marker":       marker,
			"evaluated_at": timeutil.Format(at),
			"status":       string(status),
			"reason":       string(reason),
			"config":       explainConfigJSON(record),
		})
	}
}

// explainConfigJSON renders the configuration the decision was based on. It
// is null when no effective configuration existed (none recorded, or the
// restored version is a tombstone); empty window endpoints stay null.
func explainConfigJSON(record *store.ConfigRecord) any {
	if record == nil || record.Tombstone {
		return nil
	}
	return gin.H{
		"version":    record.Version,
		"enabled":    record.Enabled,
		"percentage": record.Percentage,
		"window":     windowJSON(record.StartsAt, record.EndsAt),
	}
}
