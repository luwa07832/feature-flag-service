package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func conditionalInput(flagKey, env string, expected *string) PutConfigConditionalInput {
	return PutConfigConditionalInput{
		FlagKey:         flagKey,
		Environment:     env,
		ExpectedVersion: expected,
		Enabled:         true,
		Percentage:      25,
	}
}

func TestPutConfigConditionalMatchesEffectiveVersion(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	st, clock := openAt(t, base)
	mustEnv(t, st, "prod")
	mustFlag(t, st, "checkout")

	// No records yet: the effective version is nil, so only a nil
	// expectation may append.
	stale := "cfg-00000000000000000001-0001"
	if _, err := st.PutConfigConditional(conditionalInput("checkout", "prod", &stale)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	first, err := st.PutConfigConditional(conditionalInput("checkout", "prod", nil))
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	// The effective version moved: nil and stale expectations conflict and
	// append nothing.
	if _, err := st.PutConfigConditional(conditionalInput("checkout", "prod", nil)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if _, err := st.PutConfigConditional(conditionalInput("checkout", "prod", &stale)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}

	clock.advance(base.Add(time.Hour))
	second, err := st.PutConfigConditional(conditionalInput("checkout", "prod", &first.Version))
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.Version == first.Version {
		t.Fatalf("version not regenerated: %s", second.Version)
	}

	// A tombstone clears the effective version again.
	if _, err := st.DeleteConfig("checkout", "prod"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.PutConfigConditional(conditionalInput("checkout", "prod", &second.Version)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict (tombstone is not an effective version)", err)
	}
	if _, err := st.PutConfigConditional(conditionalInput("checkout", "prod", nil)); err != nil {
		t.Fatalf("append after tombstone: %v", err)
	}

	// Only the successful appends and the tombstone were recorded.
	history, err := st.ConfigHistory("checkout", "prod")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("history len = %d, want 4 (3 appends + 1 tombstone)", len(history))
	}
}

func TestPutConfigConditionalConcurrentSameVersion(t *testing.T) {
	base := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	st, _ := openAt(t, base)
	mustEnv(t, st, "prod")
	mustFlag(t, st, "checkout")

	const contenders = 8
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, errs[slot] = st.PutConfigConditional(conditionalInput("checkout", "prod", nil))
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrVersionConflict):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want exactly 1", succeeded)
	}
	history, err := st.ConfigHistory("checkout", "prod")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want exactly 1 appended version", len(history))
	}
}
