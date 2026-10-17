package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestClientResolverBoundsConcurrentDistinctLookups(t *testing.T) {
	resolver := newClientResolver(true)
	started := make(chan struct{}, 65)
	release := make(chan struct{})
	var workers sync.WaitGroup
	t.Cleanup(func() {
		close(release)
		workers.Wait()
	})
	resolver.lookupFn = func(string, time.Duration) (string, error) {
		started <- struct{}{}
		<-release
		return "nas.lan", nil
	}
	for index := 0; index < 64; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			resolver.resolve(fmt.Sprintf("192.0.2.%d", index+1))
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("lookup did not start below the limit")
		}
	}

	done := make(chan bool, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, ok := resolver.resolve("192.0.2.100")
		done <- ok
	}()
	select {
	case <-started:
		t.Fatal("started DNS work beyond the 64-lookup limit")
	case ok := <-done:
		if ok {
			t.Fatal("overloaded lookup should fall back to the IP")
		}
	case <-time.After(time.Second):
		t.Fatal("overloaded lookup should not wait for capacity")
	}
	if resolver.hasKey("192.0.2.100") {
		t.Fatal("overload must not be cached as a DNS failure")
	}
}

func TestClientResolverEvictsExpiredFailureBehindFreshSuccess(t *testing.T) {
	resolver := newClientResolver(true)
	resolver.maxEntries = 2
	resolver.store("192.0.2.1", "nas.lan")
	resolver.store("192.0.2.2", "")
	resolver.ageKey("192.0.2.1", 3*time.Minute)
	resolver.ageKey("192.0.2.2", 3*time.Minute)
	resolver.store("192.0.2.3", "new.lan")

	if host, ok := resolver.cached("192.0.2.1"); !ok || host != "nas.lan" {
		t.Fatal("fresh success evicted while an expired failure existed")
	}
	if resolver.hasKey("192.0.2.2") || resolver.cacheSize() != 2 {
		t.Fatal("expired failure must be removed and cache stay at capacity")
	}
}

func TestClientResolverRechecksCacheBeforeStartingLookup(t *testing.T) {
	resolver := newClientResolver(true)
	for _, hostname := range []string{"nas.lan", ""} {
		resolver.store("192.0.2.1", hostname)
		calls := 0
		resolver.lookupFn = func(string, time.Duration) (string, error) {
			calls++
			return "unexpected.lan", nil
		}

		got, err := resolver.lookupOnce("192.0.2.1")
		if err != nil || got != hostname || calls != 0 {
			t.Fatalf("lookup after another caller populated cache = (%q, %v), calls = %d; want (%q, nil), no calls", got, err, calls, hostname)
		}
	}
}
