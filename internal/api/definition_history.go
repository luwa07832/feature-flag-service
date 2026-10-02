package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// getDefinitionHistory answers the flag-definition change audit. It is a
// pure read: nothing is appended and identical inputs produce identical
// output. Validation order is fixed: flagKey, from, to, flag existence.
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
		records, err := st.DefinitionHistory(flagKey, from, to)
		if err != nil {
			internalFailure(c)
			return
		}
		items := make([]gin.H, 0, len(records))
		for i := range records {
			items = append(items, definitionHistoryItemJSON(&records[i]))
		}
		c.JSON(http.StatusOK, gin.H{
			"flag_key": flagKey,
			"items":    items,
		})
	}
}

// definitionHistoryItemJSON renders one definition change entry. Snapshots
// contain description and labels only; labels always serialise to an array.
func definitionHistoryItemJSON(record *store.FlagDefinitionRecord) gin.H {
	return gin.H{
		"event_id":       record.EventID,
		"changed_at":     timeutil.Format(record.ChangedAt),
		"action":         record.Action,
		"changed_fields": record.ChangedFields,
		"before":         definitionSnapshotJSON(record.Before),
		"after":          definitionSnapshotJSON(record.After),
	}
}

func definitionSnapshotJSON(snapshot *store.FlagDefinitionSnapshot) any {
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
