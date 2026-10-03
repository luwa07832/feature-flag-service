package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func openConditionalStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedConditional(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.CreateEnvironment("prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFlag(CreateFlagInput{Key: "checkout"}); err != nil {
		t.Fatal(err)
	}
}

func TestConditionalReplaceCreatesAndChains(t *testing.T) {
	st := openConditionalStore(t)
	seedConditional(t, st)

	rec, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
		FlagKey: "checkout", Environment: "prod",
		ExpectedVersion: nil, Enabled: true, Percentage: 10,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Tombstone || rec.Version == "" {
		t.Fatalf("unexpected record %+v", rec)
	}

	if _, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
		FlagKey: "checkout", Environment: "prod",
		ExpectedVersion: nil, Enabled: true, Percentage: 20,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("null expected with live config err = %v, want ErrVersionConflict", err)
	}

	second, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
		FlagKey: "checkout", Environment: "prod",
		ExpectedVersion: &rec.Version, Enabled: false, Percentage: 20,
	})
	if err != nil {
		t.Fatalf("chain replace: %v", err)
	}
	if second.Version == rec.Version {
		t.Fatal("replace reused version")
	}

	stale := rec.Version
	if _, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
		FlagKey: "checkout", Environment: "prod",
		ExpectedVersion: &stale, Enabled: true, Percentage: 30,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale expected err = %v, want ErrVersionConflict", err)
	}

	history, err := st.ConfigHistory("checkout", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
}

func TestConditionalReplaceAfterTombstone(t *testing.T) {
	st := openConditionalStore(t)
	seedConditional(t, st)

	first, err := st.PutConfig(PutConfigInput{
		FlagKey: "checkout", Environment: "prod", Enabled: true, Percentage: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteConfig("checkout", "prod"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
		FlagKey: "checkout", Environment: "prod",
		ExpectedVersion: &first.Version, Enabled: true, Percentage: 20,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected tombstone conflict, got %v", err)
	}

	rec, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
		FlagKey: "checkout", Environment: "prod",
		ExpectedVersion: nil, Enabled: true, Percentage: 20,
	})
	if err != nil {
		t.Fatalf("recreate after tombstone: %v", err)
	}
	if rec.Tombstone || rec.Percentage != 20 {
		t.Fatalf("unexpected record %+v", rec)
	}
}

func TestConditionalReplaceConcurrentSingleWinner(t *testing.T) {
	st := openConditionalStore(t)
	seedConditional(t, st)
	first, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
		FlagKey: "checkout", Environment: "prod",
		ExpectedVersion: nil, Enabled: true, Percentage: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	const n = 32
	var wg sync.WaitGroup
	var winners, conflicts int64
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
				FlagKey: "checkout", Environment: "prod",
				ExpectedVersion: &first.Version, Enabled: true, Percentage: 50,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, ErrVersionConflict):
				conflicts++
			default:
				t.Errorf("unexpected err: %v", err)
			}
		}()
	}
	wg.Wait()

	if winners != 1 || conflicts != n-1 {
		t.Fatalf("winners = %d conflicts = %d, want 1 and %d", winners, conflicts, n-1)
	}
	history, err := st.ConfigHistory("checkout", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
}

func TestConditionalReplaceConcurrentAgainstPutAndDelete(t *testing.T) {
	st := openConditionalStore(t)
	seedConditional(t, st)
	first, err := st.PutConfig(PutConfigInput{
		FlagKey: "checkout", Environment: "prod", Enabled: true, Percentage: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wg sync.WaitGroup
	var successes, conflicts int64
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.ConditionalReplaceConfig(ConditionalConfigInput{
				FlagKey: "checkout", Environment: "prod",
				ExpectedVersion: &first.Version, Enabled: true, Percentage: 50,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrVersionConflict):
				conflicts++
			default:
				t.Errorf("unexpected err: %v", err)
			}
		}(i)
		if i%4 == 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = st.PutConfig(PutConfigInput{
					FlagKey: "checkout", Environment: "prod", Enabled: false, Percentage: 0,
				})
			}()
		}
	}
	wg.Wait()

	if successes+conflicts != n {
		t.Fatalf("successes = %d conflicts = %d, total want %d", successes, conflicts, n)
	}
	if successes > 1 {
		t.Fatalf("successes = %d, want at most 1 against concurrent writers", successes)
	}
}
