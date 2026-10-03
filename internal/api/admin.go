package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

type createKeyRequest struct {
	Key string `json:"key"`
}

type putConfigRequest struct {
	Enabled    bool    `json:"enabled"`
	Percentage int     `json:"percentage"`
	Window     *window `json:"window"`
}

type window struct {
	StartsAt *string `json:"starts_at"`
	EndsAt   *string `json:"ends_at"`
}

// conditionalConfigRequest is the compare-and-swap configuration write body.
// ExpectedVersion keeps the raw JSON so an explicit null ("no effective
// configuration expected") stays distinguishable from a missing field, which
// is invalid. Enabled and Percentage are pointers so missing fields fail
// validation instead of silently defaulting.
type conditionalConfigRequest struct {
	ExpectedVersion json.RawMessage `json:"expected_version"`
	Enabled         *bool           `json:"enabled"`
	Percentage      *int            `json:"percentage"`
	Window          *window         `json:"window"`
}

func createEnvironment(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createKeyRequest
		if !decodeStrict(c, &req) || !validKey(c, req.Key) {
			return
		}
		env, err := st.CreateEnvironment(req.Key)
		if err != nil {
			writeStoreError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"key":        env.Key,
			"created_at": timeutil.Format(env.CreatedAt),
		})
	}
}

func putConfig(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, environment, flagKey) {
			return
		}
		var req putConfigRequest
		if !decodeStrict(c, &req) {
			return
		}
		if req.Percentage < 0 || req.Percentage > 100 {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "percentage must be between 0 and 100")
			return
		}
		startsAt, endsAt, ok := parseWindow(c, req.Window)
		if !ok {
			return
		}
		if !referencedResourcesExist(c, st, environment, flagKey) {
			return
		}
		record, err := st.PutConfig(store.PutConfigInput{
			FlagKey:     flagKey,
			Environment: environment,
			Enabled:     req.Enabled,
			Percentage:  req.Percentage,
			StartsAt:    startsAt,
			EndsAt:      endsAt,
		})
		if err != nil {
			writeStoreError(c, err)
			return
		}
		c.JSON(http.StatusCreated, configJSON(*record))
	}
}

func deleteConfig(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, environment, flagKey) {
			return
		}
		if !referencedResourcesExist(c, st, environment, flagKey) {
			return
		}
		record, err := st.DeleteConfig(flagKey, environment)
		if err != nil {
			writeStoreError(c, err)
			return
		}
		c.JSON(http.StatusOK, configJSON(*record))
	}
}

// postConfigConditional is the concurrency-safe configuration replacement
// entry: it appends exactly one immutable version, but only when
// expected_version still matches the current effective configuration version
// at execution time (a tombstone on top counts as no effective version).
// Validation order is fixed: identifiers, request body, environment, flag,
// then the version check inside the serialized store write.
func postConfigConditional(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		environment := c.Param("environment")
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, environment, flagKey) {
			return
		}
		var req conditionalConfigRequest
		if !decodeStrict(c, &req) {
			return
		}
		if req.ExpectedVersion == nil || req.Enabled == nil || req.Percentage == nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "expected_version, enabled and percentage are required")
			return
		}
		if *req.Percentage < 0 || *req.Percentage > 100 {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "percentage must be between 0 and 100")
			return
		}
		var expectedVersion *string
		if !bytes.Equal(bytes.TrimSpace(req.ExpectedVersion), []byte("null")) {
			var version string
			if err := json.Unmarshal(req.ExpectedVersion, &version); err != nil {
				fail(c, http.StatusBadRequest, codeInvalidRequest, "expected_version must be a string or null")
				return
			}
			expectedVersion = &version
		}
		startsAt, endsAt, ok := parseWindow(c, req.Window)
		if !ok {
			return
		}
		if !referencedResourcesExist(c, st, environment, flagKey) {
			return
		}
		record, err := st.PutConfigConditional(store.PutConfigConditionalInput{
			FlagKey:         flagKey,
			Environment:     environment,
			ExpectedVersion: expectedVersion,
			Enabled:         *req.Enabled,
			Percentage:      *req.Percentage,
			StartsAt:        startsAt,
			EndsAt:          endsAt,
		})
		if err != nil {
			writeStoreError(c, err)
			return
		}
		c.JSON(http.StatusCreated, configJSON(*record))
	}
}

func decodeStrict(c *gin.Context, dst any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "request body is required")
			return false
		}
		fail(c, http.StatusBadRequest, codeInvalidRequest, "request body could not be parsed")
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "request body must contain a single JSON object")
		return false
	}
	return true
}

func validKey(c *gin.Context, key string) bool {
	if KeyPattern.MatchString(key) {
		return true
	}
	fail(c, http.StatusBadRequest, codeInvalidRequest, "key must match [a-z0-9_-]{1,64}")
	return false
}

func validPathKeys(c *gin.Context, keys ...string) bool {
	for _, key := range keys {
		if !KeyPattern.MatchString(key) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "environment and flag keys must match [a-z0-9_-]{1,64}")
			return false
		}
	}
	return true
}

func parseWindow(c *gin.Context, w *window) (*int64, *int64, bool) {
	if w == nil {
		return nil, nil, true
	}
	var startsAt, endsAt *int64
	if w.StartsAt != nil {
		parsed, err := timeutil.Parse(*w.StartsAt)
		if err != nil {
			fail(c, http.StatusBadRequest, codeInvalidTimestamp, "window.starts_at must be an RFC 3339 timestamp")
			return nil, nil, false
		}
		startsAt = &parsed
	}
	if w.EndsAt != nil {
		parsed, err := timeutil.Parse(*w.EndsAt)
		if err != nil {
			fail(c, http.StatusBadRequest, codeInvalidTimestamp, "window.ends_at must be an RFC 3339 timestamp")
			return nil, nil, false
		}
		endsAt = &parsed
	}
	if startsAt != nil && endsAt != nil && *endsAt <= *startsAt {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "window.ends_at must be later than window.starts_at")
		return nil, nil, false
	}
	return startsAt, endsAt, true
}

func referencedResourcesExist(c *gin.Context, st *store.Store, environment, flagKey string) bool {
	envExists, err := st.EnvironmentExists(environment)
	if err != nil {
		internalFailure(c)
		return false
	}
	if !envExists {
		fail(c, http.StatusNotFound, codeEnvironmentNotFound, "the environment does not exist")
		return false
	}
	flagExists, err := st.FlagExists(flagKey)
	if err != nil {
		internalFailure(c)
		return false
	}
	if !flagExists {
		fail(c, http.StatusNotFound, codeFlagNotFound, "the flag does not exist")
		return false
	}
	return true
}

func writeStoreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		fail(c, http.StatusConflict, codeAlreadyExists, "the resource already exists")
	case errors.Is(err, store.ErrVersionConflict):
		fail(c, http.StatusConflict, codeVersionConflict, "the expected version does not match the current effective configuration version")
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, codeFlagNotFound, "no live configuration exists for the flag in the environment")
	default:
		fail(c, http.StatusInternalServerError, "internal_error", "the service could not complete the request")
	}
}
