package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
)

// definitionLimits are the published bounds for a flag definition: the
// trimmed description is 1-512 Unicode characters and the deduplicated label
// set holds at most 20 entries.
const (
	maxDescriptionRunes = 512
	maxLabels           = 20
)

type putFlagDefinitionRequest struct {
	Description *string   `json:"description"`
	Labels      *[]string `json:"labels"`
}

// definitionFields validates and normalizes the description/labels pair.
// When required is true both fields must be present (PUT semantics); when
// false, absent fields fall back to the empty definition (POST semantics).
// Labels are deduplicated and sorted lexicographically.
func definitionFields(c *gin.Context, description *string, labels *[]string, required bool) (string, []string, bool) {
	if required && (description == nil || labels == nil) {
		fail(c, http.StatusBadRequest, codeInvalidRequest, "description and labels are required")
		return "", nil, false
	}
	normalizedDescription := ""
	if description != nil {
		trimmed := strings.TrimSpace(*description)
		runes := utf8.RuneCountInString(trimmed)
		if runes < 1 || runes > maxDescriptionRunes {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "description must be 1 to 512 characters after trimming")
			return "", nil, false
		}
		normalizedDescription = trimmed
	}
	normalizedLabels := []string{}
	if labels != nil {
		seen := make(map[string]struct{}, len(*labels))
		for _, label := range *labels {
			if !LabelPattern.MatchString(label) {
				fail(c, http.StatusBadRequest, codeInvalidRequest, "labels must match [a-z0-9_-]{1,32}")
				return "", nil, false
			}
			if _, duplicate := seen[label]; duplicate {
				continue
			}
			seen[label] = struct{}{}
			normalizedLabels = append(normalizedLabels, label)
		}
		if len(normalizedLabels) > maxLabels {
			fail(c, http.StatusBadRequest, codeInvalidRequest, "at most 20 labels are allowed")
			return "", nil, false
		}
		sort.Strings(normalizedLabels)
	}
	return normalizedDescription, normalizedLabels, true
}

func getFlag(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, flagKey) {
			return
		}
		flag, err := st.GetFlag(flagKey)
		if err != nil {
			writeFlagLookupError(c, err)
			return
		}
		c.JSON(http.StatusOK, flagDetailJSON(*flag))
	}
}

// putFlagDefinition replaces a flag's whole definition. It never appends a
// configuration version: the per-environment history is untouched.
func putFlagDefinition(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		flagKey := c.Param("flagKey")
		if !validPathKeys(c, flagKey) {
			return
		}
		var req putFlagDefinitionRequest
		if !decodeStrict(c, &req) {
			return
		}
		description, labels, ok := definitionFields(c, req.Description, req.Labels, true)
		if !ok {
			return
		}
		flag, err := st.ReplaceFlagDefinition(flagKey, description, labels)
		if err != nil {
			writeFlagLookupError(c, err)
			return
		}
		c.JSON(http.StatusOK, flagDetailJSON(*flag))
	}
}

// listFlagDefinitions is the text-and-label search over flag definitions.
// q is a case-insensitive substring match on key or description (a blank q
// counts as absent); every label query value must be present on the flag.
// Results are ordered by key ascending.
func listFlagDefinitions(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		var needle string
		if raw := c.Query("q"); strings.TrimSpace(raw) != "" {
			needle = strings.ToLower(raw)
		}
		wantedLabels, hasLabels := c.GetQueryArray("label")
		if hasLabels {
			for _, label := range wantedLabels {
				if !LabelPattern.MatchString(label) {
					fail(c, http.StatusBadRequest, codeInvalidRequest, "label query values must match [a-z0-9_-]{1,32}")
					return
				}
			}
		}
		flags, err := st.ListFlags()
		if err != nil {
			internalFailure(c)
			return
		}
		results := make([]gin.H, 0, len(flags))
		for _, flag := range flags {
			if needle != "" &&
				!strings.Contains(strings.ToLower(flag.Key), needle) &&
				!strings.Contains(strings.ToLower(flag.Description), needle) {
				continue
			}
			if hasLabels && !hasAllLabels(flag.Labels, wantedLabels) {
				continue
			}
			results = append(results, flagDetailJSON(flag))
		}
		c.JSON(http.StatusOK, gin.H{"flags": results})
	}
}

func hasAllLabels(owned, wanted []string) bool {
	for _, want := range wanted {
		found := false
		for _, have := range owned {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func writeFlagLookupError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, codeFlagNotFound, "the flag does not exist")
		return
	}
	internalFailure(c)
}
