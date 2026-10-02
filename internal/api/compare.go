package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/eval"
	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// comparisonOutcome is the fixed vocabulary of the cross-environment compare
// surface: same config, different configs, only one side configured, or
// neither side configured at the queried time.
const (
	comparisonSame             = "same"
	comparisonDifferent        = "different"
	comparisonOnlySource       = "only_source"
	comparisonOnlyTarget       = "only_target"
	comparisonUnconfiguredBoth = "unconfigured_both"
)

// compareFlag answers a same-instant configuration comparison of one flag
// across two environments. It is a pure read: it never appends history and
// identical inputs produce identical output. Each side restores the last
// version whose changed_at is not later than at (same-instant ties broken by
// write order); a tombstone or no record means no effective configuration.
// Validation order is fixed: flagKey path, source/target, at, marker,
// source environment, target environment, flag.
func compareFlag(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flagKey := c.Param("flagKey")
		if !KeyPattern.MatchString(flagKey) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "flag key must match [a-z0-9_-]{1,64}")
			return
		}
		source := c.Query("source")
		target := c.Query("target")
		if source == "" || target == "" ||
			!KeyPattern.MatchString(source) || !KeyPattern.MatchString(target) ||
			source == target {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "source and target must be distinct valid environment keys")
			return
		}
		at, err := timeutil.Parse(c.Query("at"))
		if err != nil {
			fail(c, http.StatusBadRequest, codeInvalidTimestamp, "at must be an RFC 3339 timestamp")
			return
		}
		rawMarker := c.Query("marker")
		if err := eval.ValidateMarker(rawMarker); err != nil {
			fail(c, http.StatusBadRequest, codeInvalidMarker, "marker must match [a-z0-9_-]{1,64}")
			return
		}
		sourceExists, err := st.EnvironmentExists(source)
		if err != nil {
			internalFailure(c)
			return
		}
		if !sourceExists {
			fail(c, http.StatusNotFound, codeEnvironmentNotFound, "the environment does not exist")
			return
		}
		targetExists, err := st.EnvironmentExists(target)
		if err != nil {
			internalFailure(c)
			return
		}
		if !targetExists {
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

		fields := compareChangedFields(sourceLive, targetLive)
		c.JSON(http.StatusOK, gin.H{
			"source":         compareConfigJSON(sourceLive),
			"target":         compareConfigJSON(targetLive),
			"flag_key":       flagKey,
			"at":             timeutil.Format(at),
			"marker":         markerOrNull(rawMarker),
			"changed_fields": fields,
			"comparison":     compareOutcome(sourceLive, targetLive, fields),
			"source_status":  compareStatus(source, sourceLive, rawMarker, at),
			"target_status":  compareStatus(target, targetLive, rawMarker, at),
		})
	}
}

// liveRecord drops a restored record when it is missing or a tombstone: both
// mean there is no effective configuration at the queried time.
func liveRecord(record *store.ConfigRecord) *store.ConfigRecord {
	if record == nil || record.Tombstone {
		return nil
	}
	return record
}

// compareConfigJSON renders the restored configuration object with version;
// nil becomes null. Empty window endpoints stay null.
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

// compareChangedFields lists field differences in the fixed
// enabled/percentage/window order; version never participates. With one
// configured side it lists that side's populated fields; with neither side
// configured it is an empty array.
func compareChangedFields(source, target *store.ConfigRecord) []string {
	fields := make([]string, 0, 4)
	switch {
	case source != nil && target != nil:
		fields = append(fields, changedFields(source, target)...)
	case source != nil:
		fields = append(fields, nonNullChangeFields(source)...)
	case target != nil:
		fields = append(fields, nonNullChangeFields(target)...)
	}
	return fields
}

// compareOutcome classifies the pair of restored configurations.
func compareOutcome(source, target *store.ConfigRecord, fields []string) string {
	switch {
	case source == nil && target == nil:
		return comparisonUnconfiguredBoth
	case source == nil:
		return comparisonOnlyTarget
	case target == nil:
		return comparisonOnlySource
	case len(fields) == 0:
		return comparisonSame
	default:
		return comparisonDifferent
	}
}

// compareStatus resolves the definitive on/off/unconfigured status for one
// side when a marker is supplied; without a marker every side is
// not_evaluated.
func compareStatus(environment string, record *store.ConfigRecord, marker string, at int64) string {
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
