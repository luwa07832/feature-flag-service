package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// Change actions published by the cross-flag audit feed.
const (
	actionCreated = "created"
	actionUpdated = "updated"
	actionDeleted = "deleted"
)

// getChanges serves the cross-flag change audit feed. It is a pure read:
// nothing is appended and identical queries always return identical results.
// Validation order is fixed: path parameters, from, to, flagKey, then
// resource existence.
func getChanges(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		if !validPathKeys(c, environment) {
			return
		}
		from, ok := optionalTimestamp(c, "from")
		if !ok {
			return
		}
		to, ok := optionalTimestamp(c, "to")
		if !ok {
			return
		}
		if from != nil && to != nil && !(*from < *to) {
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
		exists, err := st.EnvironmentExists(environment)
		if err != nil {
			internalFailure(c)
			return
		}
		if !exists {
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
		records, err := st.EnvironmentConfigHistory(environment, flagKey)
		if err != nil {
			internalFailure(c)
			return
		}
		items := make([]gin.H, 0, len(records))
		for _, change := range deriveChanges(records) {
			if from != nil && change.record.ChangedAt < *from {
				continue
			}
			if to != nil && change.record.ChangedAt >= *to {
				continue
			}
			items = append(items, change.json())
		}
		c.JSON(http.StatusOK, gin.H{
			"environment": environment,
			"items":       items,
		})
	}
}

// optionalTimestamp parses an optional RFC 3339 query parameter; a provided
// but unparseable value fails the request with InvalidTimestamp.
func optionalTimestamp(c *gin.Context, name string) (*int64, bool) {
	raw, provided := c.GetQuery(name)
	if !provided {
		return nil, true
	}
	parsed, err := timeutil.Parse(raw)
	if err != nil {
		fail(c, http.StatusBadRequest, codeInvalidTimestamp, name+" must be an RFC 3339 timestamp")
		return nil, false
	}
	return &parsed, true
}

// configChange is one audit entry derived from a flag's append-only history.
// The action is classified against the flag's own predecessor in storage
// order, never against the filtered result window.
type configChange struct {
	record store.ConfigRecord
	action string
	before *store.ConfigRecord
}

// deriveChanges walks records in storage order and classifies each one. A
// live record with no live predecessor is a creation, a live record
// following a live one is an update, and a tombstone deletes the last live
// record of its flag.
func deriveChanges(records []store.ConfigRecord) []configChange {
	changes := make([]configChange, 0, len(records))
	lastLive := make(map[string]*store.ConfigRecord)
	for i := range records {
		record := &records[i]
		previous := lastLive[record.FlagKey]
		if record.Tombstone {
			changes = append(changes, configChange{record: *record, action: actionDeleted, before: previous})
			lastLive[record.FlagKey] = nil
			continue
		}
		action := actionCreated
		if previous != nil {
			action = actionUpdated
		}
		changes = append(changes, configChange{record: *record, action: action, before: previous})
		lastLive[record.FlagKey] = record
	}
	return changes
}

// changedFields lists audited fields in the published fixed order: enabled,
// percentage, window.starts_at, window.ends_at. Creations and deletions list
// the non-empty fields of the configuration they introduce or remove;
// updates list the fields that actually differ.
func (change configChange) changedFields() []string {
	fields := make([]string, 0, 4)
	switch change.action {
	case actionCreated:
		return nonEmptyFields(&change.record, fields)
	case actionDeleted:
		return nonEmptyFields(change.before, fields)
	default:
		before := change.before
		after := &change.record
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
}

func nonEmptyFields(record *store.ConfigRecord, fields []string) []string {
	if record == nil {
		return fields
	}
	if record.Enabled {
		fields = append(fields, "enabled")
	}
	if record.Percentage != 0 {
		fields = append(fields, "percentage")
	}
	if record.StartsAt != nil {
		fields = append(fields, "window.starts_at")
	}
	if record.EndsAt != nil {
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

func (change configChange) json() gin.H {
	var before, after any
	if change.before != nil {
		before = changeConfigJSON(change.before)
	}
	if change.action != actionDeleted {
		after = changeConfigJSON(&change.record)
	}
	return gin.H{
		"flag_key":       change.record.FlagKey,
		"version":        change.record.Version,
		"changed_at":     timeutil.Format(change.record.ChangedAt),
		"action":         change.action,
		"changed_fields": change.changedFields(),
		"before":         before,
		"after":          after,
	}
}

// changeConfigJSON renders the audited configuration snapshot: enabled,
// percentage and the time window, with empty window endpoints kept as null.
func changeConfigJSON(record *store.ConfigRecord) gin.H {
	return gin.H{
		"enabled":    record.Enabled,
		"percentage": record.Percentage,
		"window":     windowJSON(record.StartsAt, record.EndsAt),
	}
}
