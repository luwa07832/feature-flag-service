package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// getDefinitionHistory answers the append-only definition change query for
// one flag. It is a pure read: history is never appended and identical
// inputs produce identical output. Validation order is fixed: flagKey,
// from, to, flag existence.
func getDefinitionHistory(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flagKey := c.Param("flagKey")
		if !validKey(c, flagKey) {
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
		exists, err := st.FlagExists(flagKey)
		if err != nil {
			internalFailure(c)
			return
		}
		if !exists {
			fail(c, http.StatusNotFound, codeFlagNotFound, "the flag does not exist")
			return
		}
		events, err := st.DefinitionHistory(flagKey, from, to)
		if err != nil {
			internalFailure(c)
			return
		}
		items := make([]gin.H, 0, len(events))
		for i := range events {
			items = append(items, definitionEventJSON(&events[i]))
		}
		c.JSON(http.StatusOK, gin.H{
			"flag_key": flagKey,
			"items":    items,
		})
	}
}

// definitionEventJSON renders one definition change. Before is null for
// created events; after always carries description and labels; changed
// fields always serialise to an array.
func definitionEventJSON(event *store.DefinitionEvent) gin.H {
	fields := event.ChangedFields
	if fields == nil {
		fields = []string{}
	}
	return gin.H{
		"event_id":       event.EventID,
		"changed_at":     timeutil.Format(event.ChangedAt),
		"action":         event.Action,
		"changed_fields": fields,
		"before":         definitionSnapshotJSON(event.Before),
		"after":          definitionSnapshotJSON(event.After),
	}
}

func definitionSnapshotJSON(snapshot *store.DefinitionSnapshot) any {
	if snapshot == nil {
		return nil
	}
	labels := snapshot.Labels
	if labels == nil {
		labels = []string{}
	}
	return gin.H{
		"description": snapshot.Description,
		"labels":      labels,
	}
}
