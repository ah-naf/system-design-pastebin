# Phase 5: Caching Hardening Implementation Plan

> **Workflow note (overrides the standard executing-plans flow):** This project uses test-first pairing. Claude writes ONLY test files (and this plan). The user implements 100% of application/implementation code themselves — no exceptions for "small" or "infra-like" changes. After each task's test is written and confirmed failing, hand off to the user, then review their implementation (read the file, run tests/`go vet`) before moving to the next task.

**Goal:** Prevent cache-stampede load on DB/S3 when many concurrent requests miss the read-service cache for the same paste ID, and configure Redis eviction so the cache degrades predictably under memory pressure.

**Architecture:** Add a short-TTL Redis distributed lock (`AcquireLock`/`ReleaseLock`) to the existing `read-service/internal/cache.Cache` type. The read-service handler tries to acquire the lock on a cache miss; the winner does the existing DB+S3 fetch path, everyone else polls the cache briefly and falls back to the same fetch path if the poll budget expires. Separately, configure the dev Redis container with `maxmemory` + `allkeys-lru` eviction.

**Tech Stack:** Go 1.26 stdlib, `github.com/redis/go-redis/v9` (already a dependency), Docker Compose.

## Global Constraints

- Redis remains a non-critical dependency: any Redis failure (including lock acquisition) must degrade to the existing direct-DB+S3 behavior — never a hard failure, never an unbounded block.
- The cache-aside read path (cache → DB → S3, with negative caching) stays as-is; this phase only adds coordination around the existing Miss branch.
- Interface-seam pattern: every dependency the handler uses is an interface (`CacheGetter`, `CacheSetter`, `Repository`, `StoreRepository`), with fakes in unit tests and the real `*cache.Cache` in production.
- Claude writes only test files; the user implements all production code (`cache.go`, `handler.go`, `docker-compose.yml`).

---

### Task 1: Redis distributed lock methods on `Cache`

**Files:**
- Modify: `read-service/internal/cache/cache.go`
- Test: `read-service/internal/cache/cache_test.go`

**Interfaces:**
- Consumes: existing `Cache` struct (`client *redis.Client`), existing `NewCache(client *redis.Client) *Cache`.
- Produces:
  - `func (c *Cache) AcquireLock(ctx context.Context, id string) (bool, error)` — `true` if this call acquired the lock, `false` if another holder already has it, non-nil `error` only when Redis itself failed.
  - `func (c *Cache) ReleaseLock(ctx context.Context, id string) error` — clears the lock; safe to call even if never acquired or already expired.
  - Lock key: `"paste:lock:" + id`, 5 second TTL.

- [ ] **Step 1: Write the failing tests**

Add to `read-service/internal/cache/cache_test.go` (append at the end of the file, same package, using the existing `requireRedis` helper already defined there):

```go
func TestAcquireLockMutualExclusion(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-lock-" + t.Name()
	t.Cleanup(func() {
		client.Del(context.Background(), "paste:lock:"+id)
	})

	c := NewCache(client)

	first, err := c.AcquireLock(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireLock() first call error: %v", err)
	}
	if !first {
		t.Fatal("AcquireLock() first call = false, want true")
	}

	second, err := c.AcquireLock(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireLock() second call error: %v", err)
	}
	if second {
		t.Error("AcquireLock() second call = true, want false (lock already held)")
	}
}

func TestReleaseLockClearsKey(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-lock-release-" + t.Name()
	t.Cleanup(func() {
		client.Del(context.Background(), "paste:lock:"+id)
	})

	c := NewCache(client)

	acquired, err := c.AcquireLock(context.Background(), id)
	if err != nil || !acquired {
		t.Fatalf("AcquireLock() = (%v, %v), want (true, nil)", acquired, err)
	}

	if err := c.ReleaseLock(context.Background(), id); err != nil {
		t.Fatalf("ReleaseLock() error: %v", err)
	}

	reacquired, err := c.AcquireLock(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireLock() after release error: %v", err)
	}
	if !reacquired {
		t.Error("AcquireLock() after ReleaseLock = false, want true (lock was cleared)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd read-service && go test ./internal/cache/... -run 'TestAcquireLockMutualExclusion|TestReleaseLockClearsKey' -v`
Expected: build FAIL — `c.AcquireLock undefined` / `c.ReleaseLock undefined` (methods don't exist yet). If Redis isn't running locally, start it first: `cd infra && docker compose up -d redis`.

- [ ] **Step 3: User implements `AcquireLock`/`ReleaseLock` in `cache.go`**

Suggested shape (user writes this, not Claude):

```go
func (c *Cache) AcquireLock(ctx context.Context, id string) (bool, error) {
	key := "paste:lock:" + id
	ok, err := c.client.SetNX(ctx, key, "1", 5*time.Second).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

func (c *Cache) ReleaseLock(ctx context.Context, id string) error {
	key := "paste:lock:" + id
	return c.client.Del(ctx, key).Err()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd read-service && go test ./internal/cache/... -v`
Expected: PASS — all cache tests, including the two new ones and the pre-existing ones.

- [ ] **Step 5: Commit**

```bash
git add read-service/internal/cache/cache.go read-service/internal/cache/cache_test.go
git commit -m "feat: add Redis distributed lock methods to read-service cache"
```

---

### Task 2: Stampede-protected miss handling in the read-service handler

**Files:**
- Modify: `read-service/internal/handler/handler.go`
- Test: `read-service/internal/handler/handler_test.go`

**Interfaces:**
- Consumes: `cache.Cache.AcquireLock(ctx, id) (bool, error)` and `cache.Cache.ReleaseLock(ctx, id) error` from Task 1.
- Produces:
  - `CacheGetter` interface gains `AcquireLock(ctx context.Context, id string) (bool, error)`.
  - `CacheSetter` interface gains `ReleaseLock(ctx context.Context, id string) error`.
  - `Handler` struct gains two unexported fields, `pollInterval time.Duration` and `pollBudget time.Duration`, set by `New()` to `50 * time.Millisecond` and `1 * time.Second` respectively. Tests (same package) override these directly on the struct to keep the poll loop fast in tests.
  - `Handler.GetPaste` behavior on a cache Miss: try `AcquireLock`; if acquired, run the existing DB+S3 path and `ReleaseLock` via `defer`; if contended (`false, nil`), poll the cache every `pollInterval` up to `pollBudget` and serve directly from a `Hit`/`Negative` result if one appears, otherwise fall through to the existing DB+S3 path; if `AcquireLock` returns an error, skip locking entirely and fall through to the existing DB+S3 path immediately.

- [ ] **Step 1: Write the failing tests**

Replace the `fakeCacheGetter` and `fakeCacheSetter` types near the top of `read-service/internal/handler/handler_test.go` with these extended versions (they stay backward compatible with all existing tests in the file — `content`/`result` alone still work when `results` is left nil):

```go
type fakeCacheGetter struct {
	content []byte
	result  cache.Result

	// results/contents, when non-empty, override content/result on a
	// per-call basis (0-indexed); once exhausted, the last entry repeats.
	// Used to simulate the cache becoming populated partway through polling.
	results  []cache.Result
	contents [][]byte
	getCalls int

	acquireLockResult bool
	acquireLockErr    error
	acquireLockCalls  int
}

func (f *fakeCacheGetter) Get(ctx context.Context, id string) ([]byte, cache.Result) {
	defer func() { f.getCalls++ }()

	if len(f.results) == 0 {
		return f.content, f.result
	}

	idx := f.getCalls
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}

	var c []byte
	if idx < len(f.contents) {
		c = f.contents[idx]
	}
	return c, f.results[idx]
}

func (f *fakeCacheGetter) AcquireLock(ctx context.Context, id string) (bool, error) {
	f.acquireLockCalls++
	return f.acquireLockResult, f.acquireLockErr
}

type fakeCacheSetter struct {
	positiveCalled bool
	positiveTTL    time.Duration
	negativeCalled bool

	releaseLockCalled bool
}

func (f *fakeCacheSetter) SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration) {
	f.positiveCalled = true
	f.positiveTTL = ttl
}

func (f *fakeCacheSetter) SetNegative(ctx context.Context, id string, ttl time.Duration) {
	f.negativeCalled = true
}

func (f *fakeCacheSetter) ReleaseLock(ctx context.Context, id string) error {
	f.releaseLockCalled = true
	return nil
}
```

Then append these new test functions to the same file:

```go
func TestGetPasteLockAcquiredRunsMissPathAndReleasesLock(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss, acquireLockResult: true}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{meta: &db.PasteMeta{S3Key: "abc123"}}
	store := &fakeStore{content: "hello from S3"}
	h := New(cacheGet, cacheSet, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "hello from S3" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello from S3")
	}
	if cacheGet.acquireLockCalls != 1 {
		t.Errorf("AcquireLock calls = %d, want 1", cacheGet.acquireLockCalls)
	}
	if !cacheSet.positiveCalled {
		t.Error("cache.SetPositive was not called after lock-acquired miss path")
	}
	if !cacheSet.releaseLockCalled {
		t.Error("cache.ReleaseLock was not called after the lock-acquired path completed")
	}
}

func TestGetPasteLockErrorFallsThroughWithoutPolling(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss, acquireLockErr: errors.New("redis down")}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{meta: &db.PasteMeta{S3Key: "abc123"}}
	store := &fakeStore{content: "hello from S3"}
	h := New(cacheGet, cacheSet, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "hello from S3" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello from S3")
	}
	if cacheGet.getCalls != 1 {
		t.Errorf("Get calls = %d, want 1 (no polling attempted when AcquireLock errors)", cacheGet.getCalls)
	}
	if cacheSet.releaseLockCalled {
		t.Error("cache.ReleaseLock was called despite AcquireLock never succeeding")
	}
}

func TestGetPasteContendedPollHitServesFromCache(t *testing.T) {
	cacheGet := &fakeCacheGetter{
		acquireLockResult: false,
		results:           []cache.Result{cache.Miss, cache.Miss, cache.Hit},
		contents:          [][]byte{nil, nil, []byte("polled content")},
	}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{err: errors.New("repo must not be called when poll finds a Hit")}
	store := &fakeStore{err: errors.New("store must not be called when poll finds a Hit")}
	h := New(cacheGet, cacheSet, repo, store)
	h.pollInterval = time.Millisecond
	h.pollBudget = 20 * time.Millisecond

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "polled content" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "polled content")
	}
	if store.called {
		t.Error("store.Get was called despite the poll loop finding a Hit")
	}
}

func TestGetPasteContendedPollNegativeReturns404(t *testing.T) {
	cacheGet := &fakeCacheGetter{
		acquireLockResult: false,
		results:           []cache.Result{cache.Miss, cache.Negative},
	}
	repo := &fakeRepo{err: errors.New("repo must not be called when poll finds Negative")}
	store := &fakeStore{err: errors.New("store must not be called when poll finds Negative")}
	h := New(cacheGet, &fakeCacheSetter{}, repo, store)
	h.pollInterval = time.Millisecond
	h.pollBudget = 20 * time.Millisecond

	rec := doGetPaste(h, "missing123")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if store.called {
		t.Error("store.Get was called despite the poll loop finding Negative")
	}
}

func TestGetPasteContendedTimeoutFallsThroughToMissPath(t *testing.T) {
	cacheGet := &fakeCacheGetter{
		acquireLockResult: false,
		results:           []cache.Result{cache.Miss}, // stays Miss for every call
	}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{meta: &db.PasteMeta{S3Key: "abc123"}}
	store := &fakeStore{content: "hello from S3"}
	h := New(cacheGet, cacheSet, repo, store)
	h.pollInterval = time.Millisecond
	h.pollBudget = 10 * time.Millisecond

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "hello from S3" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello from S3")
	}
	if !cacheSet.positiveCalled {
		t.Error("cache.SetPositive was not called after the poll budget expired and the miss path ran")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd read-service && go test ./internal/handler/... -v`
Expected: build succeeds (the fakes compile fine on their own), but the five new tests FAIL on assertions — e.g. `AcquireLock calls = 0, want 1` and `cache.ReleaseLock was not called` — because `GetPaste` doesn't call `AcquireLock`/`ReleaseLock` or poll yet. All pre-existing tests in the file must still PASS unchanged.

- [ ] **Step 3: User implements the interface and handler changes in `handler.go`**

Suggested shape (user writes this, not Claude):

```go
type CacheGetter interface {
	Get(ctx context.Context, id string) ([]byte, cache.Result)
	AcquireLock(ctx context.Context, id string) (bool, error)
}

type CacheSetter interface {
	SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration)
	SetNegative(ctx context.Context, id string, ttl time.Duration)
	ReleaseLock(ctx context.Context, id string) error
}

type Handler struct {
	cacheGetter  CacheGetter
	cacheSetter  CacheSetter
	repo         Repository
	storeRepo    StoreRepository
	pollInterval time.Duration
	pollBudget   time.Duration
}

func New(cacheGetter CacheGetter, cacheSetter CacheSetter, repo Repository, storeRepo StoreRepository) *Handler {
	return &Handler{
		cacheGetter:  cacheGetter,
		cacheSetter:  cacheSetter,
		repo:         repo,
		storeRepo:    storeRepo,
		pollInterval: 50 * time.Millisecond,
		pollBudget:   1 * time.Second,
	}
}

func (h *Handler) GetPaste(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	content, result := h.cacheGetter.Get(ctx, id)
	switch result {
	case cache.Hit:
		writePlainText(w, content)
		return
	case cache.Negative:
		http.NotFound(w, r)
		return
	case cache.Miss:
		// Continue to db
	}

	acquired, lockErr := h.cacheGetter.AcquireLock(ctx, id)
	if lockErr == nil && acquired {
		defer h.cacheSetter.ReleaseLock(ctx, id)
	} else if lockErr == nil && !acquired {
		if served := h.waitForCache(ctx, w, r, id); served {
			return
		}
	}
	// lockErr != nil (Redis unavailable): fall through directly, same as a plain miss

	meta, err := h.repo.GetPaste(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			h.cacheSetter.SetNegative(ctx, id, 1*time.Minute)
			http.NotFound(w, r)
			return
		}

		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	body, size, err := h.storeRepo.Get(ctx, meta.S3Key)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", formatContentLength(size))
	w.WriteHeader(http.StatusOK)

	var buf bytes.Buffer
	tee := io.TeeReader(body, &buf)

	_, err = io.Copy(w, tee)
	if err != nil {
		return
	}

	ttl := 1 * time.Hour
	if meta.ExpiresAt != nil {
		remaining := time.Until(*meta.ExpiresAt)
		if remaining <= 0 {
			return
		}

		if remaining < ttl {
			ttl = remaining
		}
	}

	h.cacheSetter.SetPositive(ctx, id, buf.Bytes(), ttl)
}

// waitForCache polls the cache while another request holds the lock for id.
// Returns true if it already wrote a response (Hit or Negative), false if
// the poll budget expired and the caller should fall through to the normal
// miss path.
func (h *Handler) waitForCache(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) bool {
	deadline := time.Now().Add(h.pollBudget)
	for time.Now().Before(deadline) {
		time.Sleep(h.pollInterval)
		content, result := h.cacheGetter.Get(ctx, id)
		switch result {
		case cache.Hit:
			writePlainText(w, content)
			return true
		case cache.Negative:
			http.NotFound(w, r)
			return true
		}
	}
	return false
}

func writePlainText(w http.ResponseWriter, content []byte) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
```

Note this also refactors the pre-existing `cache.Hit` branch to call the new `writePlainText` helper instead of inlining the same three lines twice — both call sites (the initial `cache.Hit` case and the poll loop's `cache.Hit` case) must produce identical behavior for the existing `TestGetPasteContentTypeIsPlainText` test to keep passing.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd read-service && go test ./internal/handler/... -v`
Expected: PASS — all tests in the file, old and new.

Then run the full read-service suite plus `go vet`:

Run: `cd read-service && go vet ./... && go build ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add read-service/internal/handler/handler.go read-service/internal/handler/handler_test.go
git commit -m "feat: add stampede-protected cache miss handling to read-service handler"
```

---

### Task 3: Redis eviction config for the dev stack

**Files:**
- Modify: `infra/docker-compose.yml`

**Interfaces:**
- Consumes: nothing (pure infra config, no application code).
- Produces: the `redis` service in `infra/docker-compose.yml` runs with `maxmemory 256mb` and `maxmemory-policy allkeys-lru`.

This task has no automated test — it's a one-line `command:` override on the `redis` service. Verification is manual, via `redis-cli CONFIG GET`.

- [ ] **Step 1: User adds the `command` override**

In `infra/docker-compose.yml`, the `redis` service currently reads:

```yaml
  redis:
    image: redis:7-alpine
    ports:
      - "127.0.0.1:6379:6379"
    volumes:
      - redisdata:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
```

Add a `command:` line so it becomes:

```yaml
  redis:
    image: redis:7-alpine
    command: redis-server --maxmemory 256mb --maxmemory-policy allkeys-lru
    ports:
      - "127.0.0.1:6379:6379"
    volumes:
      - redisdata:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
```

- [ ] **Step 2: Recreate the container and verify the config**

Run:
```bash
cd infra
docker compose up -d --force-recreate redis
docker compose exec redis redis-cli CONFIG GET maxmemory
docker compose exec redis redis-cli CONFIG GET maxmemory-policy
```
Expected output:
```
1) "maxmemory"
2) "268435456"
1) "maxmemory-policy"
2) "allkeys-lru"
```
(`268435456` is 256MB in bytes — Redis always reports `CONFIG GET` values normalized to bytes.)

- [ ] **Step 3: Commit**

```bash
git add infra/docker-compose.yml
git commit -m "chore: configure Redis maxmemory and allkeys-lru eviction for dev stack"
```

---

## Self-Review

**Spec coverage:**
- Same-key stampede protection via Redis distributed lock → Task 1 (lock primitives) + Task 2 (handler wiring). ✓
- Lock TTL 5s, key `paste:lock:{id}` → Task 1. ✓
- Handler 4-case flow (acquired / error / contended-poll-hit / contended-timeout-fallback) → Task 2, all four cases have dedicated tests. ✓
- Redis error during `AcquireLock` never blocks, falls straight through → `TestGetPasteLockErrorFallsThroughWithoutPolling`. ✓
- Poll interval ~50ms, budget ~1s in production, overridable in tests → `Handler.pollInterval`/`pollBudget` fields, defaulted in `New()`, overridden directly in tests (same package). ✓
- Redis eviction config (`maxmemory` 256mb, `allkeys-lru`) → Task 3. ✓
- No general cross-key backpressure/rate-limiting (explicitly out of scope) → not implemented, correctly excluded. ✓

**Placeholder scan:** no TBD/TODO, all code blocks are complete and runnable.

**Type consistency:** `AcquireLock(ctx context.Context, id string) (bool, error)` and `ReleaseLock(ctx context.Context, id string) error` are identical across Task 1 (cache.go), Task 2's interface declarations, and Task 2's fakes/tests. `Result` values (`cache.Miss`, `cache.Hit`, `cache.Negative`) match the existing `cache.go` enum unchanged.
