# Load Balancer Implementation Plan

> **Workflow note (overrides the standard executing-plans flow):** This project uses test-first pairing. Claude writes ONLY test files (and this plan). The user implements 100% of application/implementation code themselves — no exceptions for "small" or "infra-like" changes. After each task's test is written and confirmed failing, hand off to the user, then review their implementation (read the file, run tests/`go vet`) before moving to the next task.

**Goal:** Build a load balancer that routes requests to the write-service or read-service by method+path, round-robins across replicas of each, and skips/retries around dead ones.

**Architecture:** New `lb/` directory (own `cmd/lb/main.go` + `internal/` packages, run the same way as the other services — `go run ./cmd/lb`). `internal/pool` tracks backend health and hands out the next healthy one round-robin; `internal/proxy` wraps `httputil.ReverseProxy` per request with a one-retry-on-connect-failure policy; `internal/config` reads the LB's own small env-var config; `cmd/lb/main.go` wires it all together with the same graceful-shutdown pattern used elsewhere in this project.

**Tech Stack:** Go 1.26 stdlib only — `net/http`, `net/http/httputil`, `net/url`, `sync/atomic`, `net/http/httptest` for tests. No new dependencies.

## Global Constraints

- Same module as the rest of the repo (`github.com/ah-naf/pastebin`); new code lives under `lb/`.
- Stdlib-first, minimal dependencies — this feature needs none beyond what's already used elsewhere (`net/http/httputil` for the reverse proxy).
- Claude writes only test files; the user implements all production code.
- Graceful shutdown via `signal.NotifyContext` + `server.Shutdown`, matching write-service/read-service/sweeper's existing pattern.
- No new docker-compose service — write-service/read-service aren't containerized yet, so the LB is run as a plain Go binary against whatever `host:port` addresses the operator gives it, same as the other services.

---

### Task 1: Backend pool with round-robin selection — DONE

**Files:**
- Create: `lb/internal/pool/pool.go`
- Test: `lb/internal/pool/pool_test.go`

**Interfaces:**
- Consumes: nothing (foundational package).
- Produces:
  - `type Backend struct { Addr string; ... }` (unexported health field).
  - `func New(addrs []string) *Pool`
  - `func (p *Pool) Next() (*Backend, bool)` — round-robins among healthy backends, skipping unhealthy ones; `(nil, false)` if none are healthy or the pool is empty.
  - `func (p *Pool) HasHealthy() bool` — read-only check, does not advance the round-robin counter (used by `/healthz`, so probing it doesn't skew load balancing).
  - New backends start healthy (optimistic default) until the first health check in Task 2 runs.

- [ ] **Step 1: Write the failing tests**

Create `lb/internal/pool/pool_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd lb && go test ./internal/pool/... -v`
Expected: build FAIL — `pool.go` doesn't exist yet, so `New`/`Pool`/`Backend` are undefined.

- [ ] **Step 3: User implements `pool.go`**

Suggested shape (user writes this, not Claude):

```go
package pool

import "sync/atomic"

type Backend struct {
	Addr    string
	healthy atomic.Bool
}

type Pool struct {
	backends []*Backend
	counter  atomic.Uint64
}

func New(addrs []string) *Pool {
	backends := make([]*Backend, len(addrs))
	for i, addr := range addrs {
		b := &Backend{Addr: addr}
		b.healthy.Store(true)
		backends[i] = b
	}
	return &Pool{backends: backends}
}

func (p *Pool) Next() (*Backend, bool) {
	n := len(p.backends)
	if n == 0 {
		return nil, false
	}

	start := p.counter.Add(1)
	for i := 0; i < n; i++ {
		idx := (start + uint64(i)) % uint64(n)
		b := p.backends[idx]
		if b.healthy.Load() {
			return b, true
		}
	}
	return nil, false
}

func (p *Pool) HasHealthy() bool {
	for _, b := range p.backends {
		if b.healthy.Load() {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd lb && go test ./internal/pool/... -v`
Expected: PASS — all 8 tests.

- [ ] **Step 5: Commit**

```bash
git add lb/internal/pool/pool.go lb/internal/pool/pool_test.go
git commit -m "feat: add load balancer backend pool with round-robin selection"
```

---

### Task 2: Background health checking — DONE

**Files:**
- Create: `lb/internal/pool/healthcheck.go`
- Test: `lb/internal/pool/healthcheck_test.go`

**Interfaces:**
- Consumes: `Pool` and `Backend` from Task 1 (same package, so it can use the unexported `backends`/`healthy` fields directly).
- Produces: `func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration, client *http.Client)` — starts a background goroutine; `GET {backend.Addr}/healthz` for every backend every `interval`, a 200 marks it healthy, anything else (including a request error) marks it unhealthy. Runs an initial check immediately (not just after the first tick), and stops when `ctx` is canceled.

- [ ] **Step 1: Write the failing tests**

Create `lb/internal/pool/healthcheck_test.go`:

```go
package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartHealthChecksMarksBackendsAccordingly(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer unhealthy.Close()

	p := New([]string{healthy.URL, unhealthy.URL})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthChecks(ctx, 10*time.Millisecond, healthy.Client())

	time.Sleep(50 * time.Millisecond)

	if !p.backends[0].healthy.Load() {
		t.Error("healthy backend marked unhealthy after health checks ran")
	}
	if p.backends[1].healthy.Load() {
		t.Error("unhealthy backend (500 response) still marked healthy after health checks ran")
	}
}

func TestStartHealthChecksStopsOnContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New([]string{server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	p.StartHealthChecks(ctx, 10*time.Millisecond, server.Client())
	time.Sleep(30 * time.Millisecond) // let several checks run

	cancel()
	time.Sleep(30 * time.Millisecond) // let any in-flight check finish

	// Sentinel: the server keeps responding 200, so only a health check that
	// still runs after cancel would flip this back to true.
	p.backends[0].healthy.Store(false)

	time.Sleep(50 * time.Millisecond) // long enough for another tick if the loop didn't actually stop

	if p.backends[0].healthy.Load() {
		t.Error("a health check ran after context was canceled; StartHealthChecks should have stopped")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd lb && go test ./internal/pool/... -run TestStartHealthChecks -v`
Expected: build FAIL — `StartHealthChecks` undefined.

- [ ] **Step 3: User implements `healthcheck.go`**

Suggested shape (user writes this, not Claude):

```go
package pool

import (
	"context"
	"net/http"
	"time"
)

func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration, client *http.Client) {
	go func() {
		p.checkAll(client)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.checkAll(client)
			}
		}
	}()
}

func (p *Pool) checkAll(client *http.Client) {
	for _, b := range p.backends {
		go func(b *Backend) {
			resp, err := client.Get(b.Addr + "/healthz")
			healthy := err == nil && resp.StatusCode == http.StatusOK
			if resp != nil {
				resp.Body.Close()
			}
			b.healthy.Store(healthy)
		}(b)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd lb && go test ./internal/pool/... -v`
Expected: PASS — all tests in the package, old and new (10 total).

- [ ] **Step 5: Commit**

```bash
git add lb/internal/pool/healthcheck.go lb/internal/pool/healthcheck_test.go
git commit -m "feat: add background health checking to load balancer backend pool"
```

---

### Task 3: Reverse proxy with retry-once — DONE

**Files:**
- Create: `lb/internal/proxy/proxy.go`
- Test: `lb/internal/proxy/proxy_test.go`

**Interfaces:**
- Consumes: `pool.Pool.Next() (*pool.Backend, bool)` from Task 1.
- Produces:
  - `func New(p *pool.Pool) *Proxy`
  - `Proxy` implements `http.Handler` (`ServeHTTP(w http.ResponseWriter, r *http.Request)`).
  - Behavior: no healthy backend → 503 immediately, no proxy attempt. Otherwise proxy to the picked backend; if it fails before any response bytes are written (dial/connect failure), retry once against a different backend from `Next()`; if that also fails, 502. The request body is buffered once and replayed on retry so POST bodies survive a failed first attempt.

- [ ] **Step 1: Write the failing tests**

Create `lb/internal/proxy/proxy_test.go`:

```go
package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ah-naf/pastebin/lb/internal/pool"
)

func TestServeHTTPProxiesToHealthyBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From-Backend", "yes")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from backend"))
	}))
	defer backend.Close()

	p := pool.New([]string{backend.URL})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "hello from backend" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello from backend")
	}
	if rec.Header().Get("X-From-Backend") != "yes" {
		t.Error("backend response header was not forwarded through the proxy")
	}
}

func TestServeHTTPRetriesOnUnreachableBackend(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("from the healthy one"))
	}))
	defer healthy.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := dead.URL
	dead.Close() // nobody is listening at deadAddr now

	p := pool.New([]string{deadAddr, healthy.URL})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "from the healthy one" {
		t.Errorf("body = %q, want %q (should have retried onto the healthy backend)", rec.Body.String(), "from the healthy one")
	}
}

func TestServeHTTPReturns502WhenAllBackendsUnreachable(t *testing.T) {
	dead1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead1Addr := dead1.URL
	dead1.Close()

	dead2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead2Addr := dead2.URL
	dead2.Close()

	p := pool.New([]string{dead1Addr, dead2Addr})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestServeHTTPReturns503WhenNoHealthyBackend(t *testing.T) {
	empty := pool.New(nil)
	proxy := New(empty)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestServeHTTPForwardsRequestBodyOnRetry(t *testing.T) {
	var receivedBody string
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := dead.URL
	dead.Close()

	p := pool.New([]string{deadAddr, healthy.URL})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodPost, "/paste", strings.NewReader("paste content"))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if receivedBody != "paste content" {
		t.Errorf("body received by backend after retry = %q, want %q", receivedBody, "paste content")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd lb && go test ./internal/proxy/... -v`
Expected: build FAIL — `proxy.go` doesn't exist yet, `New`/`Proxy` undefined.

- [ ] **Step 3: User implements `proxy.go`**

Suggested shape (user writes this, not Claude):

```go
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ah-naf/pastebin/lb/internal/pool"
)

type Proxy struct {
	pool *pool.Pool
}

func New(p *pool.Pool) *Proxy {
	return &Proxy{pool: p}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		r.Body.Close()
		bodyBytes = b
	}

	p.forward(w, r, bodyBytes, 2)
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, bodyBytes []byte, attemptsLeft int) {
	backend, ok := p.pool.Next()
	if !ok {
		http.Error(w, "no healthy backend available", http.StatusServiceUnavailable)
		return
	}

	target, err := url.Parse(backend.Addr)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	rp := httputil.NewSingleHostReverseProxy(target)
	failed := false
	rp.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		failed = true
	}
	rp.ServeHTTP(w, r)

	if failed {
		if attemptsLeft > 1 {
			p.forward(w, r, bodyBytes, attemptsLeft-1)
			return
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}
}
```

Note: `ErrorHandler` only fires for failures before any response bytes are written (dial/connect/RoundTrip errors) — a body-copy failure partway through a successful response is handled separately by `httputil.ReverseProxy` internally and does not call `ErrorHandler`, so this retry can never double-write a response.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd lb && go test ./internal/proxy/... -v`
Expected: PASS — all 5 tests.

Then run `go vet` across the new `lb` code:

Run: `cd lb && go vet ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add lb/internal/proxy/proxy.go lb/internal/proxy/proxy_test.go
git commit -m "feat: add retry-once reverse proxy for load balancer"
```

---

### Task 4: LB configuration — DONE

**Files:**
- Create: `lb/internal/config/config.go`
- Test: `lb/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing (reads env vars directly, independent of Tasks 1-3).
- Produces:
  - `type Config struct { WriteBackends []string; ReadBackends []string; Port string; HealthCheckInterval time.Duration }`
  - `func Load() (Config, error)` — `WRITE_BACKENDS` and `READ_BACKENDS` (comma-separated, whitespace trimmed) are required; missing either is an error. `PORT` defaults to `"8082"`. `HEALTH_CHECK_INTERVAL` defaults to `"5s"`, parsed with `time.ParseDuration`; a malformed value is an error.

- [ ] **Step 1: Write the failing tests**

Create `lb/internal/config/config_test.go`:

```go
package config

import (
	"testing"
	"time"
)

func TestLoadRequiresWriteBackends(t *testing.T) {
	t.Setenv("READ_BACKENDS", "http://localhost:8081")
	t.Setenv("WRITE_BACKENDS", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing WRITE_BACKENDS")
	}
}

func TestLoadRequiresReadBackends(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080")
	t.Setenv("READ_BACKENDS", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing READ_BACKENDS")
	}
}

func TestLoadSplitsCommaSeparatedBackends(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080, http://localhost:8090")
	t.Setenv("READ_BACKENDS", "http://localhost:8081")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := []string{"http://localhost:8080", "http://localhost:8090"}
	if len(cfg.WriteBackends) != len(want) {
		t.Fatalf("WriteBackends = %v, want %v", cfg.WriteBackends, want)
	}
	for i, addr := range want {
		if cfg.WriteBackends[i] != addr {
			t.Errorf("WriteBackends[%d] = %q, want %q", i, cfg.WriteBackends[i], addr)
		}
	}
}

func TestLoadDefaultsPortAndHealthCheckInterval(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080")
	t.Setenv("READ_BACKENDS", "http://localhost:8081")
	t.Setenv("PORT", "")
	t.Setenv("HEALTH_CHECK_INTERVAL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Port != "8082" {
		t.Errorf("Port = %q, want %q", cfg.Port, "8082")
	}
	if cfg.HealthCheckInterval != 5*time.Second {
		t.Errorf("HealthCheckInterval = %v, want %v", cfg.HealthCheckInterval, 5*time.Second)
	}
}

func TestLoadRejectsMalformedHealthCheckInterval(t *testing.T) {
	t.Setenv("WRITE_BACKENDS", "http://localhost:8080")
	t.Setenv("READ_BACKENDS", "http://localhost:8081")
	t.Setenv("HEALTH_CHECK_INTERVAL", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed HEALTH_CHECK_INTERVAL")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd lb && go test ./internal/config/... -v`
Expected: build FAIL — `config.go` doesn't exist yet, `Load`/`Config` undefined.

- [ ] **Step 3: User implements `config.go`**

Suggested shape (user writes this, not Claude):

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	WriteBackends       []string
	ReadBackends        []string
	Port                string
	HealthCheckInterval time.Duration
}

func Load() (Config, error) {
	writeBackends := os.Getenv("WRITE_BACKENDS")
	if writeBackends == "" {
		return Config{}, errors.New("WRITE_BACKENDS is required")
	}

	readBackends := os.Getenv("READ_BACKENDS")
	if readBackends == "" {
		return Config{}, errors.New("READ_BACKENDS is required")
	}

	port := envOrDefault("PORT", "8082")

	intervalStr := envOrDefault("HEALTH_CHECK_INTERVAL", "5s")
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid HEALTH_CHECK_INTERVAL: %w", err)
	}

	return Config{
		WriteBackends:       splitAddrs(writeBackends),
		ReadBackends:        splitAddrs(readBackends),
		Port:                port,
		HealthCheckInterval: interval,
	}, nil
}

func splitAddrs(s string) []string {
	parts := strings.Split(s, ",")
	addrs := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			addrs = append(addrs, trimmed)
		}
	}
	return addrs
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd lb && go test ./internal/config/... -v`
Expected: PASS — all 5 tests.

- [ ] **Step 5: Commit**

```bash
git add lb/internal/config/config.go lb/internal/config/config_test.go
git commit -m "feat: add load balancer configuration loading"
```

---

### Task 5: Wire it together in `main.go`

**Files:**
- Create: `lb/cmd/lb/main.go`

**Interfaces:**
- Consumes: `config.Load()` (Task 4), `pool.New`/`Pool.StartHealthChecks`/`Pool.Next` (Tasks 1-2), `proxy.New` (Task 3).
- Produces: the `lb` binary. No new exported interface — this is the top-level wiring, same as `write-service/cmd/write-service/main.go` and `sweeper/cmd/sweeper/main.go` have no dedicated unit tests of their own; verification here is build + manual run.

This task has no automated test — like the other services' `main.go` files, it's pure wiring. Verification is a build check plus a manual multi-replica run.

- [ ] **Step 1: User writes `lb/cmd/lb/main.go`**

Suggested shape (user writes this, not Claude):

```go
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/ah-naf/pastebin/lb/internal/config"
	"github.com/ah-naf/pastebin/lb/internal/pool"
	"github.com/ah-naf/pastebin/lb/internal/proxy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalln(err)
	}

	writePool := pool.New(cfg.WriteBackends)
	readPool := pool.New(cfg.ReadBackends)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	healthCheckClient := &http.Client{Timeout: 3 * time.Second}
	writePool.StartHealthChecks(ctx, cfg.HealthCheckInterval, healthCheckClient)
	readPool.StartHealthChecks(ctx, cfg.HealthCheckInterval, healthCheckClient)

	mux := http.NewServeMux()
	mux.Handle("POST /paste", proxy.New(writePool))
	mux.Handle("GET /paste/{id}", proxy.New(readPool))
	mux.HandleFunc("GET /healthz", healthz(writePool, readPool))

	server := &http.Server{Addr: ":" + cfg.Port, Handler: mux}

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalln(err)
		}
	case <-ctx.Done():
		log.Println("shutting down lb")
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Fatalln("graceful shutdown failed:", err)
		}
	}
}

func healthz(writePool, readPool *pool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !writePool.HasHealthy() {
			http.Error(w, "no healthy write backend", http.StatusServiceUnavailable)
			return
		}
		if !readPool.HasHealthy() {
			http.Error(w, "no healthy read backend", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}
}
```

- [ ] **Step 2: Build check**

Run: `cd lb && go build ./...`
Expected: no errors.

- [ ] **Step 3: Manual multi-replica verification**

With the existing `infra` stack already running (`cd infra && docker compose up -d`) and `infra/.env` already configured (from earlier phases), run two write-service replicas and two read-service replicas on different ports, then the LB pointed at both:

```bash
# Terminal 1
cd write-service && PORT=8080 ID_XOR_SECRET=<your existing value> go run ./cmd/write-service

# Terminal 2 (second write replica, same DB/S3, different port)
cd write-service && PORT=8090 ID_XOR_SECRET=<your existing value> go run ./cmd/write-service

# Terminal 3
cd read-service && PORT=8081 go run ./cmd/read-service

# Terminal 4 (second read replica, different port)
cd read-service && PORT=8091 go run ./cmd/read-service

# Terminal 5
cd lb && WRITE_BACKENDS="http://localhost:8080,http://localhost:8090" READ_BACKENDS="http://localhost:8081,http://localhost:8091" go run ./cmd/lb
```

Then, from a sixth terminal:

```bash
curl -X POST http://localhost:8082/paste -d '{"content":"hello via lb"}'
curl http://localhost:8082/healthz
```

Expected: the POST succeeds and returns a paste URL; `/healthz` returns `{"status":"ok"}`. Kill one of the write-service replicas (Ctrl+C in its terminal) and repeat the POST a few times — requests should keep succeeding (served by the surviving replica), and after `HEALTH_CHECK_INTERVAL` (default 5s) the LB's own `/healthz` should still report `ok` as long as at least one replica per pool survives. Kill both write replicas and POST again — expect a 503 from the LB.

- [ ] **Step 4: Commit**

```bash
git add lb/cmd/lb/main.go
git commit -m "feat: wire up load balancer main entrypoint"
```

---

## Self-Review

**Spec coverage:**
- `lb/` directory structured like the other services, run via `go run ./cmd/lb` → Task 5. ✓
- `pool.Pool` with round-robin `Next()` skipping unhealthy backends → Task 1. ✓
- Background health checking via `/healthz` polling → Task 2. ✓
- `proxy.Proxy` wrapping `httputil.ReverseProxy` with retry-once-on-connect-failure → Task 3, including the request-body-replay detail needed for POST retries (not explicitly spelled out in the spec's data-flow section but required for correctness — flagged in Task 3's implementation notes). ✓
- LB's own config independent of `shared/config`, env vars `WRITE_BACKENDS`/`READ_BACKENDS`/`PORT`/`HEALTH_CHECK_INTERVAL` → Task 4. ✓
- Method+path routing (`POST /paste` → write pool, `GET /paste/{id}` → read pool) and aggregate `/healthz` → Task 5. ✓
- Graceful shutdown matching the existing pattern → Task 5. ✓
- No containerization, no TLS, no weighted balancing (explicitly out of scope) → not implemented, correctly excluded. ✓

**Placeholder scan:** no TBD/TODO; all code blocks are complete and runnable. The manual verification in Task 5 has one placeholder-looking token, `<your existing value>` for `ID_XOR_SECRET` — this is intentional, it's the operator's own existing secret from Phase 2/3 setup, not a spec gap.

**Type consistency:** `Pool.Next() (*Backend, bool)` and `Pool.HasHealthy() bool` (Task 1) are used with identical signatures in Task 2 (same package), Task 3 (`proxy.go` calls `p.pool.Next()`), and Task 5 (`main.go` calls `writePool.HasHealthy()`/`readPool.HasHealthy()`). `config.Config` field names (`WriteBackends`, `ReadBackends`, `Port`, `HealthCheckInterval`) match between Task 4's definition and Task 5's usage in `main.go`.
