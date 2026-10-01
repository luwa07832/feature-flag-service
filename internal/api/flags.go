package api

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/evaluate"
	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// Public error codes. The three point-in-time codes are contractually fixed.
const (
	codeInvalidTimestamp   = "InvalidTimestamp"
	codeEnvironmentMissing = "EnvironmentNotFound"
	codeInvalidMarker      = "InvalidMarker"
	codeInvalidRequest     = "invalid_request"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type windowDTO struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type writeRequest struct {
	Enabled           bool       `json:"enabled"`
	RolloutPercentage int        `json:"rollout_percentage"`
	EffectiveWindow   *windowDTO `json:"effective_window"`
	ChangedAt         string     `json:"changed_at"`
	Note              string     `json:"note"`
}

type flagResultDTO struct {
	FlagID            string     `json:"flag_id"`
	VersionID         *string    `json:"version_id"`
	Enabled           bool       `json:"enabled"`
	RolloutPercentage *int       `json:"rollout_percentage"`
	EffectiveWindow   *windowDTO `json:"effective_window"`
	Evaluated         bool       `json:"evaluated"`
	Result            string     `json:"result"`
}

type snapshotResponse struct {
	EnvironmentID string          `json:"environment_id"`
	At            string          `json:"at"`
	Marker        string          `json:"marker,omitempty"`
	Flags         []flagResultDTO `json:"flags"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func windowFor(rec store.FlagRecord) *windowDTO {
	if !rec.HasWindow {
		return nil
	}
	return &windowDTO{
		Start: timeutil.Format(rec.WindowStart()),
		End:   timeutil.Format(rec.WindowEnd()),
	}
}

func evalConfig(rec store.FlagRecord) *evaluate.Config {
	return &evaluate.Config{
		FlagID:      rec.FlagID,
		Enabled:     rec.Enabled,
		RolloutPct:  rec.RolloutPct,
		WindowStart: rec.WindowStart(),
		WindowEnd:   rec.WindowEnd(),
		HasWindow:   rec.HasWindow,
		VersionID:   rec.VersionID,
	}
}

// putFlagConfig appends a new immutable configuration version.
func putFlagConfig(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environmentID := c.Param("environmentID")
		flagID := c.Param("flagID")
		if !identifierPattern.MatchString(environmentID) || !identifierPattern.MatchString(flagID) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "environment and flag identifiers must be 1-64 letters, digits, '_', '-' or '.'")
			return
		}

		var req writeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "request body must be valid JSON matching the configuration schema")
			return
		}
		if req.RolloutPercentage < 0 || req.RolloutPercentage > 100 {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "rollout_percentage must be in 0..100")
			return
		}
		in := store.AppendInput{
			EnvironmentID: environmentID,
			FlagID:        flagID,
			Enabled:       req.Enabled,
			RolloutPct:    req.RolloutPercentage,
			Note:          req.Note,
		}
		if req.EffectiveWindow != nil {
			start, err := timeutil.Parse(req.EffectiveWindow.Start)
			if err != nil {
				fail(c, http.StatusBadRequest, codeInvalidTimestamp, "effective window start is not a valid timestamp")
				return
			}
			end, err := timeutil.Parse(req.EffectiveWindow.End)
			if err != nil {
				fail(c, http.StatusBadRequest, codeInvalidTimestamp, "effective window end is not a valid timestamp")
				return
			}
			if !start.Before(end) {
				fail(c, http.StatusBadRequest, codeInvalidRequest, "effective window start must be before window end")
				return
			}
			in.HasWindow = true
			in.WindowStart = start
			in.WindowEnd = end
		}
		if req.ChangedAt != "" {
			changedAt, err := timeutil.Parse(req.ChangedAt)
			if err != nil {
				fail(c, http.StatusBadRequest, codeInvalidTimestamp, "changed_at is not a valid timestamp")
				return
			}
			in.ChangedAt = changedAt
		} else {
			in.ChangedAt = time.Now()
		}

		rec, err := st.AppendFlagChange(c.Request.Context(), in)
		if err != nil {
			fail(c, http.StatusInternalServerError, "storage_unavailable", "could not record the configuration change")
			return
		}
		c.JSON(http.StatusCreated, toChangeDTO(rec))
	}
}

type changeDTO struct {
	VersionID         string     `json:"version_id"`
	EnvironmentID     string     `json:"environment_id"`
	FlagID            string     `json:"flag_id"`
	Enabled           bool       `json:"enabled"`
	RolloutPercentage int        `json:"rollout_percentage"`
	EffectiveWindow   *windowDTO `json:"effective_window"`
	ChangedAt         string     `json:"changed_at"`
	Note              string     `json:"note"`
}

func toChangeDTO(rec store.FlagRecord) changeDTO {
	return changeDTO{
		VersionID:         rec.VersionID,
		EnvironmentID:     rec.EnvironmentID,
		FlagID:            rec.FlagID,
		Enabled:           rec.Enabled,
		RolloutPercentage: rec.RolloutPct,
		EffectiveWindow:   windowFor(rec),
		ChangedAt:         timeutil.Format(rec.ChangedAt()),
		Note:              rec.Note,
	}
}

// getFlagHistory returns the change record of one flag, newest change first.
func getFlagHistory(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environmentID := c.Param("environmentID")
		flagID := c.Param("flagID")
		records, err := st.History(c.Request.Context(), environmentID, flagID)
		if err != nil {
			fail(c, http.StatusInternalServerError, "storage_unavailable", "could not read the change history")
			return
		}
		changes := make([]changeDTO, 0, len(records))
		for _, rec := range records {
			changes = append(changes, toChangeDTO(rec))
		}
		c.JSON(http.StatusOK, gin.H{
			"environment_id": environmentID,
			"flag_id":        flagID,
			"changes":        changes,
		})
	}
}

type evaluateResponse struct {
	EnvironmentID     string     `json:"environment_id"`
	FlagID            string     `json:"flag_id"`
	VersionID         *string    `json:"version_id"`
	Enabled           bool       `json:"enabled"`
	RolloutPercentage *int       `json:"rollout_percentage"`
	EffectiveWindow   *windowDTO `json:"effective_window"`
	Marker            string     `json:"marker,omitempty"`
	EvaluatedAt       string     `json:"evaluated_at"`
	Evaluated         bool       `json:"evaluated"`
	Result            string     `json:"result"`
}

// getFlagEvaluation evaluates one flag for the current instant.
func getFlagEvaluation(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environmentID := c.Param("environmentID")
		flagID := c.Param("flagID")
		marker := c.Query("marker")
		if marker != "" && !evaluate.MarkerValid(marker) {
			fail(c, http.StatusBadRequest, codeInvalidMarker, "marker must be 1-64 letters, digits, '_', '-' or '.'")
			return
		}

		now := time.Now()
		rec, err := st.LatestFlagConfig(c.Request.Context(), environmentID, flagID)
		if err != nil && !errors.Is(err, store.ErrEnvironmentNotFound) {
			fail(c, http.StatusInternalServerError, "storage_unavailable", "could not evaluate the flag")
			return
		}

		resp := evaluateResponse{
			EnvironmentID: environmentID,
			FlagID:        flagID,
			Marker:        marker,
			EvaluatedAt:   timeutil.Format(now),
		}
		if errors.Is(err, store.ErrEnvironmentNotFound) {
			resp.Evaluated = marker != ""
			resp.Result = func() string {
				if marker == "" {
					return evaluate.StatusUnevaluated
				}
				return evaluate.StatusUnconfigured
			}()
			c.JSON(http.StatusOK, resp)
			return
		}

		versionID := rec.VersionID
		rollout := rec.RolloutPct
		resp.VersionID = &versionID
		resp.RolloutPercentage = &rollout
		resp.EffectiveWindow = windowFor(rec)
		resp.Enabled = rec.Enabled
		_, result := evaluate.Resolve(environmentID, evalConfig(rec), now, marker)
		resp.Evaluated = marker != ""
		resp.Result = result
		c.JSON(http.StatusOK, resp)
	}
}

// getEnvironmentSnapshot restores every flag of an environment at a point in
// time and, with a marker, evaluates them at that same instant.
func getEnvironmentSnapshot(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environmentID := c.Param("environmentID")

		atRaw := c.Query("at")
		at, err := timeutil.Parse(atRaw)
		if err != nil {
			fail(c, http.StatusBadRequest, codeInvalidTimestamp, "at must be an RFC 3339 timestamp")
			return
		}
		marker := c.Query("marker")
		if marker != "" && !evaluate.MarkerValid(marker) {
			fail(c, http.StatusBadRequest, codeInvalidMarker, "marker must be 1-64 letters, digits, '_', '-' or '.'")
			return
		}

		snapshot, flagOrder, err := st.SnapshotAt(c.Request.Context(), environmentID, at)
		if err != nil {
			if errors.Is(err, store.ErrEnvironmentNotFound) {
				fail(c, http.StatusNotFound, codeEnvironmentMissing, "environment has no restorable configuration at the given point in time")
				return
			}
			fail(c, http.StatusInternalServerError, "storage_unavailable", "could not restore the configuration snapshot")
			return
		}

		flags := make([]flagResultDTO, 0, len(flagOrder))
		for _, flagID := range flagOrder {
			rec, configured := snapshot[flagID]
			item := flagResultDTO{FlagID: flagID, Evaluated: marker != ""}
			if !configured {
				item.Result = evaluate.StatusUnconfigured
				flags = append(flags, item)
				continue
			}
			versionID := rec.VersionID
			rollout := rec.RolloutPct
			item.VersionID = &versionID
			item.RolloutPercentage = &rollout
			item.EffectiveWindow = windowFor(rec)
			item.Enabled = rec.Enabled
			_, result := evaluate.Resolve(environmentID, evalConfig(rec), at, marker)
			item.Result = result
			flags = append(flags, item)
		}

		c.JSON(http.StatusOK, snapshotResponse{
			EnvironmentID: environmentID,
			At:            timeutil.Format(at),
			Marker:        marker,
			Flags:         flags,
		})
	}
}
