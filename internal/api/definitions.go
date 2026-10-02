package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
	"github.com/luwa07832/feature-flag-service/internal/timeutil"
)

// LabelPattern is the published grammar for flag definition labels.
var LabelPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

const (
	maxDescriptionRunes = 512
	maxLabels           = 20
)

// flagFields captures the editable definition fields from JSON. Pointers let
// the parsers tell an omitted field from an explicit null.
type flagFields struct {
	Key         *json.RawMessage
	Description *json.RawMessage
	Labels      *json.RawMessage
}

// definitionBody is the validated definition accepted by create and replace.
type definitionBody struct {
	key         string
	description string
	labels      []string
}

func createFlagDef(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, ok := parseFlagBody(c, true)
		if !ok {
			return
		}
		flag, err := st.CreateFlag(store.CreateFlagInput{
			Key:         body.key,
			Description: body.description,
			Labels:      body.labels,
		})
		if err != nil {
			writeStoreError(c, err)
			return
		}
		c.JSON(http.StatusCreated, flagDefinitionJSON(flag))
	}
}

func getFlagDef(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flagKey := c.Param("flagKey")
		if !validKey(c, flagKey) {
			return
		}
		flag, err := st.GetFlag(flagKey)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fail(c, http.StatusNotFound, codeFlagNotFound, "the flag does not exist")
				return
			}
			internalFailure(c)
			return
		}
		c.JSON(http.StatusOK, flagDefinitionJSON(flag))
	}
}

func putFlagDefinition(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flagKey := c.Param("flagKey")
		if !validKey(c, flagKey) {
			return
		}
		body, ok := parseFlagBody(c, false)
		if !ok {
			return
		}
		flag, err := st.UpdateFlagDefinition(flagKey, body.description, body.labels)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fail(c, http.StatusNotFound, codeFlagNotFound, "the flag does not exist")
				return
			}
			internalFailure(c)
			return
		}
		c.JSON(http.StatusOK, flagDefinitionJSON(flag))
	}
}

// searchFlagDefinitions answers the definition query. q matches key or
// description case-insensitively as a substring; every repeated label query
// parameter must be present on the flag at once. It is a pure read.
func searchFlagDefinitions(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		requiredLabels := c.QueryArray("label")
		for _, label := range requiredLabels {
			if !LabelPattern.MatchString(label) {
				fail(c, http.StatusBadRequest, codeInvalidRequest, "each label must match [a-z0-9_-]{1,32}")
				return
			}
		}
		query := strings.ToLower(strings.TrimSpace(c.Query("q")))

		flags, err := st.ListFlags()
		if err != nil {
			internalFailure(c)
			return
		}
		required := uniqueSorted(requiredLabels)
		results := make([]gin.H, 0)
		for i := range flags {
			flag := &flags[i]
			if query != "" {
				key := strings.ToLower(flag.Key)
				description := strings.ToLower(flag.Description)
				if !strings.Contains(key, query) && !strings.Contains(description, query) {
					continue
				}
			}
			if !containsAllLabels(flag.Labels, required) {
				continue
			}
			results = append(results, flagDefinitionJSON(flag))
		}
		c.JSON(http.StatusOK, gin.H{"definitions": results})
	}
}

// parseFlagBody strictly decodes one JSON object and validates the definition
// fields. requireKey distinguishes registration (key mandatory) from whole
// replacement (description and labels mandatory, key rejected).
func parseFlagBody(c *gin.Context, requireKey bool) (definitionBody, bool) {
	var fields flagFields
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		if errors.Is(err, io.EOF) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "request body is required")
			return definitionBody{}, false
		}
		fail(c, http.StatusBadRequest, codeInvalidRequest, "request body could not be parsed")
		return definitionBody{}, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "request body must contain a single JSON object")
		return definitionBody{}, false
	}
	return validateFlagFields(c, fields, requireKey)
}

func validateFlagFields(c *gin.Context, fields flagFields, requireKey bool) (definitionBody, bool) {
	var body definitionBody
	if requireKey {
		if fields.Key == nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "key is required")
			return definitionBody{}, false
		}
		if err := json.Unmarshal(*fields.Key, &body.key); err != nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "key must be a string matching [a-z0-9_-]{1,64}")
			return definitionBody{}, false
		}
		if !KeyPattern.MatchString(body.key) {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "key must match [a-z0-9_-]{1,64}")
			return definitionBody{}, false
		}
	} else if fields.Key != nil {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "key cannot be changed by a definition update")
		return definitionBody{}, false
	}

	body.description = ""
	if fields.Description != nil {
		if err := json.Unmarshal(*fields.Description, &body.description); err != nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "description must be a string")
			return definitionBody{}, false
		}
		body.description = strings.TrimSpace(body.description)
	} else if !requireKey {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "description is required")
		return definitionBody{}, false
	}
	// Registration lets description stay empty only when omitted; every
	// supplied value (and replacement's mandatory one) must survive trimming
	// within the published bounds.
	if fields.Description != nil && !validDescription(body.description) {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "description must be 1 to 512 Unicode characters after trimming")
		return definitionBody{}, false
	}

	body.labels = []string{}
	if fields.Labels != nil {
		if err := json.Unmarshal(*fields.Labels, &body.labels); err != nil {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "labels must be an array of strings matching [a-z0-9_-]{1,32}")
			return definitionBody{}, false
		}
		for _, label := range body.labels {
			if !LabelPattern.MatchString(label) {
				fail(c, http.StatusBadRequest, codeInvalidRequest, "each label must match [a-z0-9_-]{1,32}")
				return definitionBody{}, false
			}
		}
		body.labels = uniqueSorted(body.labels)
		if len(body.labels) > maxLabels {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "labels must contain at most 20 unique values")
			return definitionBody{}, false
		}
	} else if !requireKey {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "labels is required")
		return definitionBody{}, false
	}
	return body, true
}

func validDescription(description string) bool {
	length := utf8.RuneCountInString(description)
	return length >= 1 && length <= maxDescriptionRunes
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

func containsAllLabels(have, required []string) bool {
	for _, label := range required {
		if !containsString(have, label) {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	i := sort.SearchStrings(values, target)
	return i < len(values) && values[i] == target
}

// flagDefinitionJSON renders one flag definition detail. Labels always
// serialise to an array, never null.
func flagDefinitionJSON(flag *store.Flag) gin.H {
	labels := flag.Labels
	if labels == nil {
		labels = []string{}
	}
	return gin.H{
		"key":         flag.Key,
		"description": flag.Description,
		"labels":      labels,
		"created_at":  timeutil.Format(flag.CreatedAt),
		"updated_at":  timeutil.Format(flag.UpdatedAt),
	}
}
