package quota

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveIsAtomicAndIdempotent(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, reserveErr := store.Reserve(context.Background(), "account", "cycle", "model")
			if reserveErr != nil {
				t.Errorf("reserve: %v", reserveErr)
			}
			if ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("expected one reservation, got %d", won.Load())
	}
}

func TestResolveCycleSurvivesResetAtDriftUntilActiveTransition(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	account := Account{Key: "account", ID: "id", Plan: "plus"}
	first := Snapshot{Eligible: true, CycleID: "cycle-a"}
	if err := store.Observe(ctx, account, first); err != nil {
		t.Fatal(err)
	}
	drifted, err := store.ResolveCycle(ctx, account.Key, Snapshot{Eligible: true, CycleID: "cycle-b"})
	if err != nil || drifted.CycleID != "cycle-a" {
		t.Fatalf("cycle drifted: %#v err=%v", drifted, err)
	}
	if err := store.Observe(ctx, account, Snapshot{Eligible: false, Reason: "window_already_active"}); err != nil {
		t.Fatal(err)
	}
	next, err := store.ResolveCycle(ctx, account.Key, Snapshot{Eligible: true, CycleID: "cycle-c"})
	if err != nil || next.CycleID != "cycle-c" {
		t.Fatalf("new cycle was not accepted: %#v err=%v", next, err)
	}
}

func TestBackoffPersists(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().UTC().Add(time.Hour).Truncate(time.Nanosecond)
	if err := store.SetBackoff(context.Background(), "account", want); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.BackoffUntil(context.Background(), "account")
	if err != nil || !got.Equal(want) {
		t.Fatalf("got=%v want=%v err=%v", got, want, err)
	}
}

func TestFailedRetriableCanBeReservedAgainButUnknownCannot(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	ok, err := store.Reserve(ctx, "account", "cycle", "model")
	if err != nil || !ok {
		t.Fatalf("first reserve: ok=%v err=%v", ok, err)
	}
	if err := store.SetCycle(ctx, "account", "cycle", "failed_retriable", 429, "activation_rate_limited"); err != nil {
		t.Fatal(err)
	}
	ok, err = store.Reserve(ctx, "account", "cycle", "model")
	if err != nil || !ok {
		t.Fatalf("retry reserve: ok=%v err=%v", ok, err)
	}
	if err := store.SetCycle(ctx, "account", "cycle", "sent_unknown", 0, "activation_delivery_unknown"); err != nil {
		t.Fatal(err)
	}
	ok, err = store.Reserve(ctx, "account", "cycle", "model")
	if err != nil || ok {
		t.Fatalf("unknown was reserved: ok=%v err=%v", ok, err)
	}
}

func TestCatalogCacheSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Now().UTC().Truncate(time.Nanosecond)
	if err := store.SaveCatalog(context.Background(), "catalog", []byte(`{"valid":true}`), "remote", fetchedAt); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw, source, gotAt, err := store.LoadCatalog(context.Background(), "catalog")
	if err != nil || string(raw) != `{"valid":true}` || source != "remote" || !gotAt.Equal(fetchedAt) {
		t.Fatalf("raw=%s source=%s fetched=%v err=%v", raw, source, gotAt, err)
	}
}
