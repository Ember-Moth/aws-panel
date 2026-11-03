package catalogcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testCache(t *testing.T) (*Cache, *atomic.Int64) {
	t.Helper()
	c := New(context.Background(), nil)
	t.Cleanup(c.Close)
	now := &atomic.Int64{}
	now.Store(time.Now().UnixNano())
	c.now = func() time.Time { return time.Unix(0, now.Load()) }
	return c, now
}
func waitRefresh(t *testing.T, c *Cache, key string) {
	t.Helper()
	c.mu.Lock()
	f := c.flights[key]
	c.mu.Unlock()
	if f != nil {
		select {
		case <-f.done:
		case <-time.After(time.Second):
			t.Fatal("refresh did not complete")
		}
	}
}
func TestFreshHitClonesAndSeparatesKeys(t *testing.T) {
	c, _ := testCache(t)
	calls := 0
	load := func(context.Context) ([]string, error) { calls++; return []string{"aws"}, nil }
	v, m, err := Read(context.Background(), c, Key("a", "east"), Products, false, load)
	if err != nil || m.Cached {
		t.Fatal(m, err)
	}
	v[0] = "modified"
	v, m, err = Read(context.Background(), c, Key("a", "east"), Products, false, load)
	if err != nil || !m.Cached || v[0] != "aws" || calls != 1 {
		t.Fatal(v, m, calls, err)
	}
	for _, key := range []string{Key("b", "east"), Key("a", "west"), Key("a", "east", "new-version")} {
		_, _, err = Read(context.Background(), c, key, Products, false, load)
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatal("scope keys shared data", calls)
	}
}
func TestStaleReturnsImmediatelyAndRefreshesOnce(t *testing.T) {
	c, now := testCache(t)
	key := Key(t.Name())
	var calls atomic.Int32
	ready := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context) (int, error) {
		n := calls.Add(1)
		if n == 2 {
			close(ready)
			<-release
		}
		return int(n), nil
	}
	first, meta, err := Read(context.Background(), c, key, Environment, false, load)
	if err != nil || first != 1 {
		t.Fatal(err)
	}
	now.Add(int64(3 * time.Minute))
	for range 10 {
		v, m, err := Read(context.Background(), c, key, Environment, false, load)
		if err != nil || v != 1 || !m.Stale || !m.Refreshing || !m.UpdatedAt.Equal(meta.UpdatedAt) {
			t.Fatal(v, m, err)
		}
	}
	<-ready
	if calls.Load() != 2 {
		t.Fatal("duplicate background calls", calls.Load())
	}
	close(release)
	waitRefresh(t, c, key)
	v, m, err := Read(context.Background(), c, key, Environment, false, load)
	if err != nil || v != 2 || m.Stale || !m.Cached {
		t.Fatal(v, m, err)
	}
}
func TestFailedRefreshRetainsSnapshotUntilHardExpiry(t *testing.T) {
	c, now := testCache(t)
	key := Key(t.Name())
	var calls atomic.Int32
	bad := errors.New("upstream failure")
	load := func(context.Context) (int, error) {
		if calls.Add(1) > 1 {
			return 0, bad
		}
		return 7, nil
	}
	_, _, _ = Read(context.Background(), c, key, Environment, false, load)
	now.Add(int64(3 * time.Minute))
	v, m, err := Read(context.Background(), c, key, Environment, false, load)
	if err != nil || v != 7 || !m.Stale {
		t.Fatal(v, m, err)
	}
	waitRefresh(t, c, key)
	v, m, err = Read(context.Background(), c, key, Environment, false, load)
	if err != nil || v != 7 || !m.RefreshFailed || m.Refreshing || calls.Load() != 2 {
		t.Fatal(v, m, calls.Load(), err)
	}
	_, _, err = Read(context.Background(), c, key, Environment, true, load)
	if !errors.Is(err, bad) {
		t.Fatal("force hid upstream error", err)
	}
	now.Add(int64(20 * time.Minute))
	_, _, err = Read(context.Background(), c, key, Environment, false, load)
	if !errors.Is(err, bad) {
		t.Fatal("hard-expired result returned", err)
	}
}
func TestConcurrentColdReadsSurviveCallerCancellation(t *testing.T) {
	c, _ := testCache(t)
	key := Key(t.Name())
	var calls atomic.Int32
	ready := make(chan struct{})
	release := make(chan struct{})
	load := func(ctx context.Context) (int, error) {
		calls.Add(1)
		close(ready)
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := Read(ctx, c, key, Products, false, load); done <- err }()
	<-ready
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("caller cancellation ignored")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _, err := Read(context.Background(), c, key, Products, false, load)
			if err != nil || v != 42 {
				t.Error(v, err)
			}
		}()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("cold miss stampede", calls.Load())
	}
}
func TestMemoryCacheBoundedAndCloseCancelsRefresh(t *testing.T) {
	c, _ := testCache(t)
	for i := range maxEntries + 20 {
		_, _, err := Read(context.Background(), c, Key(time.Duration(i).String()), Products, false, func(context.Context) (int, error) { return i, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	count := len(c.entries)
	c.mu.Unlock()
	if count != maxEntries {
		t.Fatal(count)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := Read(context.Background(), c, Key(t.Name()), Products, true, func(ctx context.Context) (int, error) { close(started); <-ctx.Done(); return 0, ctx.Err() })
		done <- err
	}()
	<-started
	c.Close()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("shutdown did not cancel loader")
	}
}

func TestIncompleteRefreshCannotReplaceCompleteSnapshot(t *testing.T) {
	c, now := testCache(t)
	key := Key(t.Name())
	type observation struct {
		Complete bool
		Value    int
	}
	var incomplete atomic.Bool
	load := func(context.Context) (observation, error) {
		if incomplete.Load() {
			return observation{Value: 0}, nil
		}
		return observation{Complete: true, Value: 42}, nil
	}
	valid := func(v observation) bool { return v.Complete }
	first, meta, err := ReadChecked(context.Background(), c, key, QuotaLimits, false, load, valid)
	if err != nil || !first.Complete {
		t.Fatal(first, err)
	}
	incomplete.Store(true)
	forced, failed, err := ReadChecked(context.Background(), c, key, QuotaLimits, true, load, valid)
	if err != nil || forced.Complete || !failed.RefreshFailed {
		t.Fatal("partial observation lost", forced, failed, err)
	}
	kept, m, err := ReadChecked(context.Background(), c, key, QuotaLimits, false, load, valid)
	if err != nil || !kept.Complete || kept.Value != 42 || !m.Cached || !m.UpdatedAt.Equal(meta.UpdatedAt) {
		t.Fatal("partial replaced complete snapshot", kept, m, err)
	}
	now.Add(int64(2 * time.Hour))
	_, _, _ = ReadChecked(context.Background(), c, key, QuotaLimits, false, load, valid)
	waitRefresh(t, c, key)
	kept, m, err = ReadChecked(context.Background(), c, key, QuotaLimits, false, load, valid)
	if err != nil || kept.Value != 42 || !m.Stale || !m.RefreshFailed || !m.UpdatedAt.Equal(meta.UpdatedAt) {
		t.Fatal("partial background refresh reset retention", kept, m, err)
	}
}
