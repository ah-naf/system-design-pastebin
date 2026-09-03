package pool

import "testing"

func TestNextRoundRobinsAmongHealthyBackends(t *testing.T) {
	p := New([]string{"http://a", "http://b", "http://c"})

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		b, ok := p.Next()
		if !ok {
			t.Fatalf("Next() call %d: ok = false, want true", i)
		}
		seen[b.Addr]++
	}

	for _, addr := range []string{"http://a", "http://b", "http://c"} {
		if seen[addr] != 2 {
			t.Errorf("backend %q selected %d times over 6 calls, want 2 (even round-robin)", addr, seen[addr])
		}
	}
}

func TestNextSkipsUnhealthyBackends(t *testing.T) {
	p := New([]string{"http://a", "http://b"})
	p.backends[0].healthy.Store(false) // mark "http://a" down

	for i := 0; i < 4; i++ {
		b, ok := p.Next()
		if !ok {
			t.Fatalf("Next() call %d: ok = false, want true", i)
		}
		if b.Addr != "http://b" {
			t.Errorf("Next() call %d = %q, want %q (only healthy backend)", i, b.Addr, "http://b")
		}
	}
}

func TestNextReturnsFalseWhenAllUnhealthy(t *testing.T) {
	p := New([]string{"http://a", "http://b"})
	p.backends[0].healthy.Store(false)
	p.backends[1].healthy.Store(false)

	if _, ok := p.Next(); ok {
		t.Error("Next() ok = true, want false (no healthy backends)")
	}
}

func TestNextReturnsFalseForEmptyPool(t *testing.T) {
	p := New(nil)

	if _, ok := p.Next(); ok {
		t.Error("Next() ok = true, want false (empty pool)")
	}
}

func TestNewBackendsStartHealthy(t *testing.T) {
	p := New([]string{"http://a"})

	if !p.backends[0].healthy.Load() {
		t.Error("new backend healthy = false, want true (optimistic default until first health check)")
	}
}

func TestHasHealthyTrueWhenAtLeastOneHealthy(t *testing.T) {
	p := New([]string{"http://a", "http://b"})
	p.backends[0].healthy.Store(false)

	if !p.HasHealthy() {
		t.Error("HasHealthy() = false, want true (one backend still healthy)")
	}
}

func TestHasHealthyFalseWhenAllUnhealthy(t *testing.T) {
	p := New([]string{"http://a"})
	p.backends[0].healthy.Store(false)

	if p.HasHealthy() {
		t.Error("HasHealthy() = true, want false (no healthy backends)")
	}
}

func TestHasHealthyFalseForEmptyPool(t *testing.T) {
	p := New(nil)

	if p.HasHealthy() {
		t.Error("HasHealthy() = true, want false (empty pool)")
	}
}
