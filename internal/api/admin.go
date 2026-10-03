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

// conditionalConfigRequest distinguishes omitted required fields from
// explicit JSON null/false/zero via the presence map populated by
// UnmarshalJSON. expected_version accepts null ("no live configuration") but
// must not be omitted.
type conditionalConfigRequest struct {
	ExpectedVersion *string
	Enabled         *bool
	Percentage      *int
	Window          *window
	present         map[string]bool
}

func (r *conditionalConfigRequest) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		return errInvalidConditionalField
	}
	r.present = map[string]bool{}
	for key := range raw {
		if !knownConditionalFields[key] {
			return errInvalidConditionalField
		}
	}
	if rawValue, ok := raw["expected_version"]; ok {
		r.present["expected_version"] = true
		trimmed := bytes.TrimSpace(rawValue)
		switch jsonType(trimmed) {
		case "null":
		case "string":
			var version string
			if err := json.Unmarshal(trimmed, &version); err != nil {
				return errInvalidConditionalField
			}
			r.ExpectedVersion = &version
		default:
			return errInvalidConditionalField
		}
	}
	if rawValue, ok := raw["enabled"]; ok {
		r.present["enabled"] = true
		trimmed := bytes.TrimSpace(rawValue)
		if jsonType(trimmed) == "bool" {
			var enabled bool
			if err := json.Unmarshal(trimmed, &enabled); err != nil {
				return errInvalidConditionalField
			}
			r.Enabled = &enabled
		} else if jsonType(trimmed) != "null" {
			return errInvalidConditionalField
		}
	}
	if rawValue, ok := raw["percentage"]; ok {
		r.present["percentage"] = true
		trimmed := bytes.TrimSpace(rawValue)
		if jsonType(trimmed) == "number" {
			var percentage int
			if err := json.Unmarshal(trimmed, &percentage); err != nil {
				return errInvalidConditionalField
			}
			r.Percentage = &percentage
		} else if jsonType(trimmed) != "null" {
			return errInvalidConditionalField
		}
	}
	if rawWindow, ok := raw["window"]; ok {
		trimmed := bytes.TrimSpace(rawWindow)
		if string(trimmed) != "null" {
			var w window
			decoder := json.NewDecoder(bytes.NewReader(trimmed))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&w); err != nil {
				return errInvalidConditionalField
			}
			r.Window = &w
		}
	}
	return nil
}

var knownConditionalFields = map[string]bool{
	"expected_version": true,
	"enabled":          true,
	"percentage":       true,
	"window":           true,
}

// jsonType classifies a trimmed JSON value by its leading token.
func jsonType(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	case '{':
		return "object"
	case '[':
		return "array"
	default:
		return "number"
	}
}

var errInvalidConditionalField = errors.New("conditional request field has the wrong type")

// jsonType panics-free reads the leading token of a non-empty trimmed value.

func replaceConfigConditional(st *store.Store) gin.HandlerFunc {
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
		expectedVersion, ok := requireExpectedVersion(c, &req)
		if !ok {
			return
		}
		if !req.present["enabled"] || req.Enabled == nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "enabled is required and must be a boolean")
			return
		}
		if !req.present["percentage"] || req.Percentage == nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "percentage is required and must be an integer between 0 and 100")
			return
		}
		if *req.Percentage < 0 || *req.Percentage > 100 {
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
		record, err := st.ConditionalReplaceConfig(store.ConditionalConfigInput{
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

// requireExpectedVersion validates the expected_version field: it must be
// present and either null (no live configuration expected) or a JSON string
// (the version read by the caller).
func requireExpectedVersion(c *gin.Context, req *conditionalConfigRequest) (*string, bool) {
	if !req.present["expected_version"] {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "expected_version is required and must be a string or null")
		return nil, false
	}
	return req.ExpectedVersion, true
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
		fail(c, http.StatusConflict, codeVersionConflict, "expected_version does not match the current live configuration version")
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, codeFlagNotFound, "no live configuration exists for the flag in the environment")
	default:
		fail(c, http.StatusInternalServerError, "internal_error", "the service could not complete the request")
	}
}
