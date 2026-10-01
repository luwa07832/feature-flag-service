package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/eval"
	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

func listFlags(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flags, err := st.ListFlags()
		if err != nil {
			internalFailure(c)
			return
		}
		keys := make([]string, 0, len(flags))
		for _, flag := range flags {
			keys = append(keys, flag.Key)
		}
		c.JSON(http.StatusOK, gin.H{"flags": keys})
	}
}

func getHistory(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, environment, flagKey) {
			return
		}
		exists, err := st.EnvironmentExists(environment)
		if err != nil {
			internalFailure(c)
			return
		}
		if !exists {
			fail(c, http.StatusNotFound, codeEnvironmentNotFound, "the environment does not exist")
			return
		}
		records, err := st.ConfigHistory(flagKey, environment)
		if err != nil {
			internalFailure(c)
			return
		}
		versions := make([]gin.H, 0, len(records))
		for _, record := range records {
			versions = append(versions, configJSON(record))
		}
		c.JSON(http.StatusOK, gin.H{
			"environment": environment,
			"flag_key":    flagKey,
			"versions":    versions,
		})
	}
}

// evaluate serves the existing real-time evaluation; it is deliberately the
// same evaluation core as the historical entry.
func evaluate(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		if !validPathKeys(c, environment) {
			return
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
		exists, err := st.EnvironmentExists(environment)
		if err != nil {
			internalFailure(c)
			return
		}
		if !exists {
			fail(c, http.StatusNotFound, codeEnvironmentNotFound, "the environment does not exist")
			return
		}
		now := st.Now()
		effective, err := st.EffectiveConfigsAt(environment, now)
		if err != nil {
			internalFailure(c)
			return
		}
		flags, err := st.ListFlags()
		if err != nil {
			internalFailure(c)
			return
		}
		results := make([]gin.H, 0, len(flags))
		for _, flag := range flags {
			record, present := effective[flag.Key]
			var live *store.ConfigRecord
			if present && !record.Tombstone {
				live = &record
			}
			results = append(results, flagJSON(environment, flag.Key, live, true, marker, now))
		}
		c.JSON(http.StatusOK, gin.H{
			"environment":  environment,
			"evaluated_at": timeutil.Format(now),
			"marker":       marker,
			"flags":        results,
		})
	}
}

// evaluateAt is the historical point-in-time restore and evaluation entry.
// Validation order is fixed so every malformed request gets a unique result:
// timestamp, then marker, then environment.
func evaluateAt(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		if !validPathKeys(c, environment) {
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
		exists, err := st.EnvironmentExists(environment)
		if err != nil {
			internalFailure(c)
			return
		}
		if !exists {
			fail(c, http.StatusNotFound, codeEnvironmentNotFound, "the environment does not exist")
			return
		}
		effective, err := st.EffectiveConfigsAt(environment, at)
		if err != nil {
			internalFailure(c)
			return
		}
		if !hasRestorableConfig(effective) {
			fail(c, http.StatusNotFound, codeEnvironmentNotFound, "no configuration can be restored for the environment at that time")
			return
		}
		flags, err := st.ListFlags()
		if err != nil {
			internalFailure(c)
			return
		}
		evaluated := rawMarker != ""
		results := make([]gin.H, 0, len(flags))
		for _, flag := range flags {
			record, present := effective[flag.Key]
			var restored *store.ConfigRecord
			if present && !record.Tombstone {
				restored = &record
			}
			results = append(results, flagJSON(environment, flag.Key, restored, evaluated, rawMarker, at))
		}
		response := gin.H{
			"environment": environment,
			"at":          timeutil.Format(at),
			"marker":      markerOrNull(rawMarker),
			"flags":       results,
		}
		c.JSON(http.StatusOK, response)
	}
}

// flagJSON renders one flag row. record is nil for the unconfigured state:
// the flag key is still emitted, while version, percentage, window and
// enabled are null. Without a marker every row carries the unified
// not_evaluated status; with a marker the status is definitive.
func flagJSON(environment, flagKey string, record *store.ConfigRecord, evaluated bool, marker string, at int64) gin.H {
	row := gin.H{"flag_key": flagKey}
	if record == nil {
		row["version"] = nil
		row["percentage"] = nil
		row["window"] = nil
		row["enabled"] = nil
		if evaluated {
			row["status"] = string(eval.StatusUnconfigured)
		} else {
			row["status"] = string(eval.StatusNotEvaluated)
		}
		return row
	}
	row["version"] = record.Version
	row["percentage"] = record.Percentage
	row["window"] = windowJSON(record.StartsAt, record.EndsAt)
	row["enabled"] = record.Enabled
	switch {
	case !evaluated:
		row["status"] = string(eval.StatusNotEvaluated)
	case record.Tombstone:
		row["status"] = string(eval.StatusUnconfigured)
	default:
		cfg := eval.Configuration{
			FlagKey:    record.FlagKey,
			Enabled:    record.Enabled,
			Percentage: record.Percentage,
			Window:     eval.Window{StartsAt: record.StartsAt, EndsAt: record.EndsAt},
		}
		row["status"] = string(eval.Evaluated(environment, &cfg, marker, at))
	}
	return row
}

func markerOrNull(marker string) any {
	if marker == "" {
		return nil
	}
	return marker
}

// explainFlag answers one deterministic flag decision for one marker. It is a
// pure read: it never appends history, and identical inputs always produce
// identical output. Without an at query parameter the decision uses the
// service's current time; with one it restores the last version whose
// changed_at is not later than at. Validation order is fixed: path, at,
// marker, environment, flag.
func explainFlag(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, environment, flagKey) {
			return
		}
		at := st.Now()
		if rawAt, provided := c.GetQuery("at"); provided {
			parsed, err := timeutil.Parse(rawAt)
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
		exists, err := st.EnvironmentExists(environment)
		if err != nil {
			internalFailure(c)
			return
		}
		if !exists {
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
		record, err := st.LatestConfigAt(flagKey, environment, at)
		if err != nil {
			internalFailure(c)
			return
		}

		var config *eval.Configuration
		var configBody any
		switch {
		case record == nil || record.Tombstone:
			configBody = nil
		default:
			config = &eval.Configuration{
				FlagKey:    record.FlagKey,
				Enabled:    record.Enabled,
				Percentage: record.Percentage,
				Window:     eval.Window{StartsAt: record.StartsAt, EndsAt: record.EndsAt},
			}
			configBody = gin.H{
				"version":    record.Version,
				"enabled":    record.Enabled,
				"percentage": record.Percentage,
				"window":     windowJSON(record.StartsAt, record.EndsAt),
			}
		}
		status, reason := eval.Explain(environment, config, marker, at)
		c.JSON(http.StatusOK, gin.H{
			"environment":  environment,
			"flag_key":     flagKey,
			"marker":       marker,
			"evaluated_at": timeutil.Format(at),
			"status":       string(status),
			"reason":       string(reason),
			"config":       configBody,
		})
	}
}

func hasRestorableConfig(effective map[string]store.ConfigRecord) bool {
	for _, record := range effective {
		if !record.Tombstone {
			return true
		}
	}
	return false
}

func internalFailure(c *gin.Context) {
	fail(c, http.StatusInternalServerError, "internal_error", "the service could not complete the request")
}
