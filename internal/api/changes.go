package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// getChanges answers the cross-flag change audit query. It is a pure read:
// history is never appended and identical inputs produce identical output.
// Validation order is fixed: path, from, to, flagKey, environment, flag.
func getChanges(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		if !validPathKeys(c, environment) {
			return
		}
		var from, to *int64
		if raw, provided := c.GetQuery("from"); provided {
			parsed, err := timeutil.Parse(raw)
			if err != nil {
				fail(c, http.StatusBadRequest, codeInvalidTimestamp, "from must be an RFC 3339 timestamp")
				return
			}
			from = &parsed
		}
		if raw, provided := c.GetQuery("to"); provided {
			parsed, err := timeutil.Parse(raw)
			if err != nil {
				fail(c, http.StatusBadRequest, codeInvalidTimestamp, "to must be an RFC 3339 timestamp")
				return
			}
			to = &parsed
		}
		if from != nil && to != nil && *from >= *to {
			fail(c, http.StatusBadRequest, codeInvalidTimestamp, "from must be earlier than to")
			return
		}
		flagKey := ""
		if raw, provided := c.GetQuery("flagKey"); provided {
			if !KeyPattern.MatchString(raw) {
				fail(c, http.StatusBadRequest, codeInvalidRequest, "flagKey must match [a-z0-9_-]{1,64}")
				return
			}
			flagKey = raw
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
		if flagKey != "" {
			flagExists, err := st.FlagExists(flagKey)
			if err != nil {
				internalFailure(c)
				return
			}
			if !flagExists {
				fail(c, http.StatusNotFound, codeFlagNotFound, "the flag does not exist")
				return
			}
		}
		records, err := st.EnvironmentChanges(environment, flagKey)
		if err != nil {
			internalFailure(c)
			return
		}
		items := make([]gin.H, 0)
		// latestValid tracks the latest non-tombstone record per flag while
		// walking the full append-only chain in (changed_at, id) order, so
		// action classification is independent of the from/to window.
		latestValid := make(map[string]*store.ConfigRecord)
		for i := range records {
			record := &records[i]
			if !withinChangeWindow(record.ChangedAt, from, to) {
				advanceChangeState(latestValid, record)
				continue
			}
			previous := latestValid[record.FlagKey]
			items = append(items, changeItemJSON(record, previous))
			advanceChangeState(latestValid, record)
		}
		c.JSON(http.StatusOK, gin.H{
			"environment": environment,
			"items":       items,
		})
	}
}

func withinChangeWindow(changedAt int64, from, to *int64) bool {
	if from != nil && changedAt < *from {
		return false
	}
	if to != nil && changedAt >= *to {
		return false
	}
	return true
}

func advanceChangeState(latestValid map[string]*store.ConfigRecord, record *store.ConfigRecord) {
	if record.Tombstone {
		latestValid[record.FlagKey] = nil
		return
	}
	latestValid[record.FlagKey] = record
}

// changeItemJSON renders one audit entry. previous is the preceding live
// (non-tombstone) configuration for the flag, or nil when there is none.
func changeItemJSON(record, previous *store.ConfigRecord) gin.H {
	var action string
	var before, after any
	var fields []string
	switch {
	case record.Tombstone:
		action = "deleted"
		before = changeConfigJSON(record)
		after = nil
		fields = nonNullChangeFields(record)
	case previous == nil:
		action = "created"
		before = nil
		after = changeConfigJSON(record)
		fields = nonNullChangeFields(record)
	default:
		action = "updated"
		before = changeConfigJSON(previous)
		after = changeConfigJSON(record)
		fields = changedFields(previous, record)
	}
	return gin.H{
		"flag_key":       record.FlagKey,
		"version":        record.Version,
		"changed_at":     timeutil.Format(record.ChangedAt),
		"action":         action,
		"changed_fields": fields,
		"before":         before,
		"after":          after,
	}
}

// changeConfigJSON renders a configuration object snapshot for before/after.
func changeConfigJSON(r *store.ConfigRecord) gin.H {
	return gin.H{
		"enabled":    r.Enabled,
		"percentage": r.Percentage,
		"window":     windowJSON(r.StartsAt, r.EndsAt),
	}
}

// nonNullChangeFields lists the populated fields in fixed order, used for
// created and deleted entries.
func nonNullChangeFields(r *store.ConfigRecord) []string {
	fields := []string{"enabled", "percentage"}
	if r.StartsAt != nil {
		fields = append(fields, "window.starts_at")
	}
	if r.EndsAt != nil {
		fields = append(fields, "window.ends_at")
	}
	return fields
}

// changedFields lists fields that differ between two consecutive live
// configurations, in the fixed enabled/percentage/window order.
func changedFields(before, after *store.ConfigRecord) []string {
	fields := make([]string, 0, 4)
	if before.Enabled != after.Enabled {
		fields = append(fields, "enabled")
	}
	if before.Percentage != after.Percentage {
		fields = append(fields, "percentage")
	}
	if !sameInstant(before.StartsAt, after.StartsAt) {
		fields = append(fields, "window.starts_at")
	}
	if !sameInstant(before.EndsAt, after.EndsAt) {
		fields = append(fields, "window.ends_at")
	}
	return fields
}

func sameInstant(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
