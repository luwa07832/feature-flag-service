package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/eval"
	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// compareFlags answers a cross-environment point-in-time comparison of one
// flag. It restores the last version whose changed_at is not later than at
// on each side (same-instant rewrites win by insertion order), reports the
// field-level diff and, when a marker is supplied, evaluates both sides. It
// is a pure read: it never appends history and identical inputs yield
// identical output. Validation order is fixed: flagKey, source, target, at,
// marker, environments, flag.
func compareFlags(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flagKey := c.Param("flagKey")
		if !KeyPattern.MatchString(flagKey) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "flag key must match [a-z0-9_-]{1,64}")
			return
		}
		source := c.Query("source")
		target := c.Query("target")
		if source == "" || target == "" ||
			!KeyPattern.MatchString(source) || !KeyPattern.MatchString(target) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "source and target must match [a-z0-9_-]{1,64}")
			return
		}
		if source == target {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "source and target must be different environments")
			return
		}
		at, err := timeutil.Parse(c.Query("at"))
		if err != nil {
			fail(c, http.StatusBadRequest, codeInvalidTimestamp, "at must be an RFC 3339 timestamp")
			return
		}
		marker := c.Query("marker")
		if err := eval.ValidateMarker(marker); err != nil {
			fail(c, http.StatusBadRequest, codeInvalidMarker, "marker must match [a-z0-9_-]{1,64}")
			return
		}
		for _, environment := range []string{source, target} {
			exists, err := st.EnvironmentExists(environment)
			if err != nil {
				internalFailure(c)
				return
			}
			if !exists {
				fail(c, http.StatusNotFound, codeEnvironmentNotFound, "the environment does not exist")
				return
			}
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

		sourceRecord, err := st.LatestConfigAt(flagKey, source, at)
		if err != nil {
			internalFailure(c)
			return
		}
		targetRecord, err := st.LatestConfigAt(flagKey, target, at)
		if err != nil {
			internalFailure(c)
			return
		}
		sourceLive := liveRecord(sourceRecord)
		targetLive := liveRecord(targetRecord)

		sourceBody := compareConfigJSON(sourceLive)
		targetBody := compareConfigJSON(targetLive)
		comparison := compareOutcome(sourceLive, targetLive)
		fields := compareChangedFields(sourceLive, targetLive)

		sourceStatus := sideStatus(source, sourceLive, marker, at)
		targetStatus := sideStatus(target, targetLive, marker, at)

		c.JSON(http.StatusOK, gin.H{
			"source":         sourceBody,
			"target":         targetBody,
			"flag_key":       flagKey,
			"at":             timeutil.Format(at),
			"marker":         markerOrNull(marker),
			"changed_fields": fields,
			"comparison":     comparison,
			"source_status":  sourceStatus,
			"target_status":  targetStatus,
		})
	}
}

// liveRecord drops tombstone records: a tombstone means no effective
// configuration existed at the queried time.
func liveRecord(record *store.ConfigRecord) *store.ConfigRecord {
	if record == nil || record.Tombstone {
		return nil
	}
	return record
}

// compareConfigJSON renders one side as a versioned configuration object or
// null when the side had no effective configuration at the queried time.
func compareConfigJSON(record *store.ConfigRecord) any {
	if record == nil {
		return nil
	}
	return gin.H{
		"version":    record.Version,
		"enabled":    record.Enabled,
		"percentage": record.Percentage,
		"window":     windowJSON(record.StartsAt, record.EndsAt),
	}
}

// compareOutcome classifies the pair into the single comparison vocabulary.
func compareOutcome(source, target *store.ConfigRecord) string {
	switch {
	case source == nil && target == nil:
		return "unconfigured_both"
	case source == nil:
		return "only_target"
	case target == nil:
		return "only_source"
	case sameConfig(source, target):
		return "same"
	default:
		return "different"
	}
}

func sameConfig(a, b *store.ConfigRecord) bool {
	return a.Enabled == b.Enabled &&
		a.Percentage == b.Percentage &&
		sameInstant(a.StartsAt, b.StartsAt) &&
		sameInstant(a.EndsAt, b.EndsAt)
}

// compareChangedFields lists differing fields in the fixed
// enabled/percentage/window order. Version never participates. When only one
// side is configured it lists that side's non-empty fields; with neither
// side configured it is an empty array.
func compareChangedFields(source, target *store.ConfigRecord) []string {
	switch {
	case source == nil && target == nil:
		return []string{}
	case source == nil:
		return nonNullChangeFields(target)
	case target == nil:
		return nonNullChangeFields(source)
	default:
		return changedFields(source, target)
	}
}

// sideStatus evaluates one side using the same decision order as every other
// evaluation surface: disabled, window, rollout. Without a marker the status
// is the unified not_evaluated state.
func sideStatus(environment string, record *store.ConfigRecord, marker string, at int64) string {
	if marker == "" {
		return string(eval.StatusNotEvaluated)
	}
	if record == nil {
		return string(eval.StatusUnconfigured)
	}
	cfg := eval.Configuration{
		FlagKey:    record.FlagKey,
		Enabled:    record.Enabled,
		Percentage: record.Percentage,
		Window:     eval.Window{StartsAt: record.StartsAt, EndsAt: record.EndsAt},
	}
	return string(eval.Evaluated(environment, &cfg, marker, at))
}
