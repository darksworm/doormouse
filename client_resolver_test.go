package main

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- clientResolver --------------------------------------------------------

// TestClientResolverDisabled checks that a disabled resolver never reaches the
// lookup function and never reports a hostname.
func TestClientResolverDisabled(t *testing.T) {
	r := newClientResolver(false)
	r.lookupFn = func(ip string, _ time.Duration) (string, error) {
		t.Fatal("lookup must not run when the resolver is disabled")
		return "", nil
	}
	if _, ok := r.resolve("192.0.2.1"); ok {
		t.Fatal("expected no hostname when the resolver is disabled")
	}
}

// TestClientResolverCachesPositiveResult checks that a successful lookup is
// remembered, that its FQDN trailing dot is dropped, and that the second
// resolve for the same IP does no further DNS work.
func TestClientResolverCachesPositiveResult(t *testing.T) {
	calls := 0
	r := newClientResolver(true)
	r.lookupFn = func(ip string, _ time.Duration) (string, error) {
		calls++
		return "nas.lan.", nil // trailing dot must be stripped
	}

	host, ok := r.resolve("192.0.2.10")
	if !ok || host != "nas.lan" {
		t.Fatalf("got (%q, %v), want (nas.lan, true)", host, ok)
	}
	if _, ok := r.resolve("192.0.2.10"); !ok {
		t.Fatal("cached hostname not returned on second resolve")
	}
	if calls != 1 {
		t.Fatalf("expected 1 lookup, got %d", calls)
	}
}

// TestClientResolverCachesNegativeResult checks that a failed lookup is cached
// so a network without PTR records is not re-queried on every request.
func TestClientResolverCachesNegativeResult(t *testing.T) {
	calls := 0
	r := newClientResolver(true)
	r.lookupFn = func(ip string, _ time.Duration) (string, error) {
		calls++
		return "", errors.New("no PTR record")
	}

	for i := 0; i < 3; i++ {
		if _, ok := r.resolve("192.0.2.11"); ok {
			t.Fatal("expected no hostname")
		}
	}
	if calls != 1 {
		t.Fatalf("expected 1 lookup (negative results are cached), got %d", calls)
	}
}

// TestClientResolverNegativeTTLExpiry checks that a remembered failure is
// retried once its shorter negative TTL has passed.
func TestClientResolverNegativeTTLExpiry(t *testing.T) {
	calls := 0
	r := newClientResolver(true)
	r.negTTL = 15 * time.Millisecond
	r.lookupFn = func(ip string, _ time.Duration) (string, error) {
		calls++
		return "", errors.New("no PTR record")
	}

	r.resolve("192.0.2.12")
	time.Sleep(30 * time.Millisecond)
	r.resolve("192.0.2.12")

	if calls != 2 {
		t.Fatalf("expected the lookup to repeat after the negative TTL, got %d calls", calls)
	}
}

// TestClientResolverDifferentIPsAreIndependent checks that caching one IP does
// not stand in for another: each distinct IP gets its own lookup.
func TestClientResolverDifferentIPsAreIndependent(t *testing.T) {
	seen := map[string]bool{}
	r := newClientResolver(true)
	r.lookupFn = func(ip string, _ time.Duration) (string, error) {
		seen[ip] = true
		return ip + ".example", nil
	}
	if _, ok := r.resolve("192.0.2.20"); !ok {
		t.Fatal("expected hostname for 192.0.2.20")
	}
	if _, ok := r.resolve("192.0.2.21"); !ok {
		t.Fatal("expected hostname for 192.0.2.21")
	}
	if len(seen) != 2 {
		t.Fatalf("expected lookups for both IPs, saw %v", seen)
	}
}

// TestClientResolverCoalescesConcurrentLookups checks the single-flight path:
// a burst of requests from one uncached IP must share a single lookup instead
// of each request starting its own.
func TestClientResolverCoalescesConcurrentLookups(t *testing.T) {
	r := newClientResolver(true)

	release := make(chan struct{})
	var calls int32
	r.lookupFn = func(ip string, _ time.Duration) (string, error) {
		atomic.AddInt32(&calls, 1)
		<-release // hold every in-flight lookup until the whole burst arrives
		return ip + ".example", nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			host, ok := r.resolve("192.0.2.30")
			if !ok || host != "192.0.2.30.example" {
				t.Errorf("resolve() = (%q, %v), want (192.0.2.30.example, true)", host, ok)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // let the burst pile onto the first lookup
	close(release)
	wg.Wait()

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected 1 lookup for 8 concurrent requests, got %d", n)
	}
	if len(r.inFlightKeys()) != 0 {
		t.Fatal("in-flight lookups must be cleared once they finish")
	}
}

// TestClientResolverEvictsOldestWhenCacheFull checks the hard cap: once the
// cache is full of entries that are all still fresh, inserting another IP
// evicts the oldest entry instead of growing the cache.
func TestClientResolverEvictsOldestWhenCacheFull(t *testing.T) {
	r := newClientResolver(true)
	r.maxEntries = 3
	r.lookupFn = func(ip string, _ time.Duration) (string, error) {
		return ip + ".example", nil
	}

	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if _, ok := r.resolve(ip); !ok {
			t.Fatalf("expected a hostname for %s", ip)
		}
	}
	// The cache is at the cap and nothing has expired, so this insert has to
	// evict rather than grow.
	if _, ok := r.resolve("192.0.2.4"); !ok {
		t.Fatal("expected a hostname for 192.0.2.4")
	}

	if got := r.cacheSize(); got != 3 {
		t.Fatalf("cache size = %d, want the cap of 3", got)
	}
	if r.hasKey("192.0.2.1") {
		t.Fatal("the oldest entry should have been evicted to make room")
	}
	for _, ip := range []string{"192.0.2.2", "192.0.2.3", "192.0.2.4"} {
		if !r.hasKey(ip) {
			t.Fatalf("%s should still be cached", ip)
		}
	}
}

// TestClientResolverPrunesExpiredBeforeEvicting checks that making room drops
// expired entries first: a fresh entry must survive when an expired one is
// available to free.
func TestClientResolverPrunesExpiredBeforeEvicting(t *testing.T) {
	r := newClientResolver(true)
	r.maxEntries = 2
	r.ttl = time.Hour

	r.store("192.0.2.1", "old.example")
	r.store("192.0.2.2", "fresh.example")
	r.ageKey("192.0.2.1", 2*time.Hour)

	r.store("192.0.2.3", "new.example")

	if got := r.cacheSize(); got != 2 {
		t.Fatalf("cache size = %d, want the cap of 2", got)
	}
	if r.hasKey("192.0.2.1") {
		t.Fatal("the expired entry should have been pruned")
	}
	if !r.hasKey("192.0.2.2") {
		t.Fatal("a fresh entry must not be evicted while an expired one can be pruned")
	}
	if !r.hasKey("192.0.2.3") {
		t.Fatal("the new entry should have been stored")
	}
}

// TestClientResolverUpdateAtCapacityDoesNotEvict checks that re-storing an IP
// that is already cached only replaces its value: it must not evict a sibling
// entry or change the cache size.
func TestClientResolverUpdateAtCapacityDoesNotEvict(t *testing.T) {
	r := newClientResolver(true)
	r.maxEntries = 2

	r.store("192.0.2.1", "first.example")
	r.store("192.0.2.2", "second.example")
	r.store("192.0.2.1", "updated.example")

	if got := r.cacheSize(); got != 2 {
		t.Fatalf("cache size = %d, want 2 (an update must not evict)", got)
	}
	if !r.hasKey("192.0.2.2") {
		t.Fatal("updating 192.0.2.1 must not evict 192.0.2.2")
	}
	if got := r.hostnameFor("192.0.2.1"); got != "updated.example" {
		t.Fatalf("hostname = %q, want updated.example", got)
	}
}

// TestClientResolverRefreshedEntryLeavesTheFrontOfOrder checks that re-storing a
// cached IP moves it to the back of the insertion order. Left at the front, a
// freshly refreshed entry stops makeRoomLocked's expired sweep dead in its
// tracks and then becomes the first entry evicted, so the one entry that had
// just been resolved again was the one dropped.
func TestClientResolverRefreshedEntryLeavesTheFrontOfOrder(t *testing.T) {
	r := newClientResolver(true)
	r.maxEntries = 3
	r.ttl = time.Hour

	r.store("192.0.2.1", "first.example")
	r.store("192.0.2.2", "second.example")
	r.store("192.0.2.3", "third.example")

	// 192.0.2.1 is the oldest entry, and it is the one refreshed.
	r.store("192.0.2.1", "renewed.example")
	r.store("192.0.2.4", "new.example")

	if got := r.cacheSize(); got != 3 {
		t.Fatalf("cache size = %d, want the cap of 3", got)
	}
	if !r.hasKey("192.0.2.1") {
		t.Fatal("the entry that had just been refreshed must not be the one evicted")
	}
	if r.hasKey("192.0.2.2") {
		t.Fatal("192.0.2.2 is now the least recently resolved entry and should have been evicted")
	}
	if got := r.hostnameFor("192.0.2.1"); got != "renewed.example" {
		t.Fatalf("hostname = %q, want renewed.example", got)
	}
}

// TestClientResolverDefaultCapIs8192 pins the production cap so a test-only
// override cannot quietly become the shipped value.
func TestClientResolverDefaultCapIs8192(t *testing.T) {
	if got := newClientResolver(true).maxEntries; got != 8192 {
		t.Fatalf("default maxEntries = %d, want 8192", got)
	}
}

// --- lookupClientAddr ------------------------------------------------------

// blockingResolver never answers a query: its dial blocks until the lookup's
// own context is done, which is exactly what a dead DNS server looks like.
func blockingResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
}

// TestLookupClientAddrHonoursTimeout checks that the timeout bounds the DNS
// lookup itself: the call must fail near the deadline rather than waiting on
// the resolver to give up on its own.
func TestLookupClientAddrHonoursTimeout(t *testing.T) {
	const timeout = 50 * time.Millisecond

	start := time.Now()
	host, err := lookupClientAddr(blockingResolver(), "192.0.2.50", timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a timeout error, got (%q, nil)", host)
	}
	if host != "" {
		t.Fatalf("hostname = %q, want empty on error", host)
	}
	if strings.Contains(err.Error(), "no DNS servers") {
		t.Skipf("host has no DNS server for the resolver to dial: %v", err)
	}
	if elapsed < timeout {
		t.Fatalf("returned after %s, before the %s deadline", elapsed, timeout)
	}
	if elapsed > 20*timeout {
		t.Fatalf("returned after %s, want it to stop near the %s deadline", elapsed, timeout)
	}
}

// TestLookupClientAddrPropagatesResolverError checks that a resolver failure is
// returned as an error, which is what puts the IP in the negative cache.
func TestLookupClientAddrPropagatesResolverError(t *testing.T) {
	unreachable := &net.Resolver{
		PreferGo: true,
		Dial: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("no such host")
		},
	}
	if _, err := lookupClientAddr(unreachable, "192.0.2.51", time.Second); err == nil {
		t.Fatal("expected an error when the lookup yields no name")
	}
}

// --- clientAddr ------------------------------------------------------------

// TestClientAddrWithHostname checks that a resolved hostname is appended to the
// remote address in the request log.
func TestClientAddrWithHostname(t *testing.T) {
	tp := newTestProxy(t, nil)
	tp.svc.clientResolver = newClientResolver(true)
	tp.svc.clientResolver.lookupFn = func(ip string, _ time.Duration) (string, error) {
		if ip == "172.23.0.3" {
			return "open-webui.", nil
		}
		return "", errors.New("no PTR record")
	}

	req := httptest.NewRequest("GET", "/api/server/ping", nil)
	req.RemoteAddr = "172.23.0.3:57648"

	if got := tp.svc.clientAddr(req); got != "172.23.0.3:57648 (hostname: open-webui)" {
		t.Errorf("clientAddr = %q", got)
	}
}

// TestClientAddrUnresolvableAndForwarded checks that a failed lookup leaves the
// plain remote address in place, and that X-Forwarded-For is still reported.
func TestClientAddrUnresolvableAndForwarded(t *testing.T) {
	tp := newTestProxy(t, nil)
	tp.svc.clientResolver = newClientResolver(true)
	tp.svc.clientResolver.lookupFn = func(ip string, _ time.Duration) (string, error) {
		return "", errors.New("no PTR record")
	}

	req := httptest.NewRequest("GET", "/api/server/ping", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	if got := tp.svc.clientAddr(req); got != "203.0.113.7:1234" {
		t.Errorf("clientAddr = %q", got)
	}

	req.Header.Set("X-Forwarded-For", "192.168.50.15")
	if got := tp.svc.clientAddr(req); got != "203.0.113.7:1234 (forwarded for 192.168.50.15)" {
		t.Errorf("clientAddr = %q", got)
	}
}

// TestClientAddrLookupDisabled checks the default path: with the resolver off,
// the log gets the bare remote address and no DNS work happens.
func TestClientAddrLookupDisabled(t *testing.T) {
	tp := newTestProxy(t, nil)
	tp.svc.clientResolver = newClientResolver(false)
	tp.svc.clientResolver.lookupFn = func(ip string, _ time.Duration) (string, error) {
		t.Fatal("lookup must not run when the resolver is disabled")
		return "", nil
	}

	req := httptest.NewRequest("GET", "/api/server/ping", nil)
	req.RemoteAddr = "172.23.0.3:57648"
	if got := tp.svc.clientAddr(req); got != "172.23.0.3:57648" {
		t.Errorf("clientAddr = %q", got)
	}
}

// TestClientIP checks that the bare IP is split off an "ip:port" remote
// address, for IPv6 as well as IPv4, and passed through when it cannot split.
func TestClientIP(t *testing.T) {
	if got := clientIP("172.23.0.3:57648"); got != "172.23.0.3" {
		t.Fatalf("clientIP = %q, want 172.23.0.3", got)
	}
	if got := clientIP("[::1]:4242"); got != "::1" {
		t.Fatalf("clientIP = %q, want ::1", got)
	}
	if got := clientIP("garbage"); got != "garbage" {
		t.Fatalf("clientIP = %q, want garbage (passthrough)", got)
	}
}

// --- config -----------------------------------------------------------------

// TestLoadConfigClientHostnameLookup checks that hostname lookup is off unless
// the key is set to true: an absent key and an explicit false both disable it,
// and only an explicit true enables it.
func TestLoadConfigClientHostnameLookup(t *testing.T) {
	cfg, err := LoadConfig(writeTempConfig(t, machinesRoutesConfig), RealClock{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClientHostnameLookup {
		t.Error("client_hostname_lookup must default to false when absent")
	}

	// The key must sit in the top-level section, before any [[machines]] table.
	disabled := "client_hostname_lookup = false\n" + strings.TrimLeft(machinesRoutesConfig, "\n")
	cfg, err = LoadConfig(writeTempConfig(t, disabled), RealClock{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClientHostnameLookup {
		t.Error("client_hostname_lookup = false must disable the lookup")
	}

	enabled := "client_hostname_lookup = true\n" + strings.TrimLeft(machinesRoutesConfig, "\n")
	cfg, err = LoadConfig(writeTempConfig(t, enabled), RealClock{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.ClientHostnameLookup {
		t.Error("client_hostname_lookup = true must enable the lookup as an explicit opt-in")
	}
}

// --- test helpers for the resolver's internals ------------------------------

// cacheSize reports how many entries the resolver currently remembers.
func (r *clientResolver) cacheSize() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cache)
}

// hasKey reports whether ip currently has an entry in the cache.
func (r *clientResolver) hasKey(ip string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.cache[ip]
	return ok
}

// hostnameFor returns the remembered hostname for ip, "" if it has none.
func (r *clientResolver) hostnameFor(ip string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cache[ip].hostname
}

// inFlightKeys lists the IPs that currently have a lookup in flight.
func (r *clientResolver) inFlightKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.inFlight))
	for k := range r.inFlight {
		keys = append(keys, k)
	}
	return keys
}

// ageKey backdates an entry's resolved time by age so it reads as expired.
func (r *clientResolver) ageKey(ip string, age time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[ip]
	if !ok {
		return
	}
	entry.resolvedAt = entry.resolvedAt.Add(-age)
	r.cache[ip] = entry
}
