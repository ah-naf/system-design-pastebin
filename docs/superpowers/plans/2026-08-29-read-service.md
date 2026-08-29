# Read Service Implementation Plan

> **Execution mode:** Task 1 is a small, targeted change to already-tested existing code (`shared/config`) — Claude does this directly, no test-first pairing, same reasoning as Phase 2's infra task. Tasks 2-5 continue the established pattern: Claude writes each task's failing test, the user writes the implementation, Claude reviews before moving on. Task 6 (main.go wiring) has no Go test file — its "test" is a manual end-to-end walkthrough (start Write Service, POST a paste, start Read Service, GET it back), run together.

**Goal:** Build the Read Service: `GET /paste/{id}` (cache → DB → S3, cache-aside + negative caching) and `GET /healthz` (readiness).

**Architecture:** Same interface-seam pattern as Write Service — `db.Repo` (Postgres, read-only), `storage.Store` (S3/MinIO, read-only), `cache.Cache` (Redis, errors never surfaced), `handler.Handler` (depends only on interfaces, unit-tested with fakes) — wired in `cmd/read-service/main.go`.

**Tech Stack:** Go 1.26 stdlib `net/http`/`database/sql`, `jackc/pgx/v5`, `aws/aws-sdk-go-v2`, `redis/go-redis/v9` — all already dependencies from Phase 1/2, no new ones.

## Global Constraints

- Module path: `github.com/ah-naf/pastebin`.
- Read Service never accepts writes — no `POST /paste` route exists in this binary (Hard Constraint #1).
- Read path is always cache → DB → S3, cache-aside population on miss, negative caching for missing/expired/deleted (Hard Constraint #3).
- Cache is never a hard dependency: every `cache.Cache` method swallows its own Redis errors internally and behaves as a miss/no-op — no method on `Cache` returns an error. The handler has no `if err != nil` branch for any cache call.
- DB and S3 failures DO fail the request (`500`) — no fallback exists for those, unlike cache.
- `db.Repo.GetPaste`'s single query filters `is_deleted = false AND (expires_at IS NULL OR expires_at > now())` — "not found," "deleted," and "expired" are indistinguishable to the caller and to the cache, by design.
- Positive cache TTL: `1*time.Hour`, or `time.Until(*ExpiresAt)` if sooner and positive. Negative cache TTL: `60*time.Second`.
- Content served as `Content-Type: text/plain; charset=utf-8`.
- `read-service/internal/storage.Store` does NOT auto-create the bucket (unlike Write Service) — a read-only service shouldn't need `CreateBucket` permission.
- Tests: stdlib `testing` only, table-driven where it fits. Integration tests (Postgres/MinIO/Redis) skip cleanly via `t.Skip` when unreachable.

---

### Task 1: Make `ID_XOR_SECRET` optional in `shared/config`

Read Service has no use for `ID_XOR_SECRET` (only Write Service's `id.Generator` needs it), but both services share `shared/config.Load()`. Rather than duplicate all the other env-var parsing in a second loader, this makes the field optional at the shared layer and moves the requirement into Write Service's own startup check.

**Files:**
- Modify: `shared/config/config.go`
- Modify: `shared/config/config_test.go`
- Modify: `write-service/cmd/write-service/main.go`

**Interfaces:**
- Produces: `Config.IDXORSecret` is `0` (zero value) when `ID_XOR_SECRET` is unset, instead of `Load()` erroring. Task 6 (`read-service/cmd/read-service/main.go`) relies on being able to call `config.Load()` without setting that env var.

- [ ] **Step 1: Claude edits `shared/config/config.go`**

Change the `ID_XOR_SECRET` handling from `requiredEnv` to optional parsing:

```go
var idXORSecret uint64
if raw := os.Getenv("ID_XOR_SECRET"); raw != "" {
	idXORSecret, err = strconv.ParseUint(raw, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid ID_XOR_SECRET: %w", err)
	}
}
```

(replacing the old `requiredEnv("ID_XOR_SECRET")` + unconditional `ParseUint` block). `idXORSecret` defaults to `0` if the block above never runs.

- [ ] **Step 2: Claude updates `shared/config/config_test.go`**

`TestLoadRequiresIDXORSecret` currently asserts `Load()` errors when `ID_XOR_SECRET` is empty — that's no longer true, so replace it:

```go
func TestLoadAllowsMissingIDXORSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ID_XOR_SECRET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with unset ID_XOR_SECRET returned error: %v", err)
	}
	if cfg.IDXORSecret != 0 {
		t.Errorf("IDXORSecret = %#x, want 0 when unset", cfg.IDXORSecret)
	}
}
```

`TestLoadRejectsMalformedXORSecret` stays as-is — a malformed (non-empty, non-hex) value must still error.

- [ ] **Step 3: Claude adds a startup check to `write-service/cmd/write-service/main.go`**

Right after `cfg, err := config.Load()` and its error check, add:

```go
if cfg.IDXORSecret == 0 {
	log.Fatalln("ID_XOR_SECRET is required for write-service")
}
```

(A genuinely-configured secret that happens to parse to exactly `0` — e.g. `"0000000000000000"` — would also trip this and be rejected as "unset." That's an accepted simplification: an all-zero XOR secret provides no obfuscation anyway, so treating it as invalid is correct, not just convenient.)

- [ ] **Step 4: Run the full test suite, confirm nothing broke**

Run: `go test ./... -count=1`
Expected: all packages `ok`, including the updated `shared/config` suite.

- [ ] **Step 5: Manually verify write-service still refuses to start without the secret**

```bash
DATABASE_URL="postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable" \
S3_ACCESS_KEY=pastebin_minio S3_SECRET_KEY=pastebin_minio_password \
go run ./write-service/cmd/write-service
```

Expected: exits immediately with `ID_XOR_SECRET is required for write-service` (no `ID_XOR_SECRET` was set in this command).

- [ ] **Step 6: Commit**

```bash
git add shared/config/config.go shared/config/config_test.go write-service/cmd/write-service/main.go
git commit -m "refactor: make ID_XOR_SECRET optional in shared config, required by write-service only"
```

---

### Task 2: `read-service/internal/db` — read-only Repo

**Files:**
- Test: `read-service/internal/db/repo_test.go`
- Implementation (user writes this): `read-service/internal/db/repo.go`

**Interfaces:**
- Consumes: `pgconn.Open`/`RunMigrations` (test setup only, same as Write Service's Repo test).
- Produces:
  ```go
  type PasteMeta struct {
      S3Key     string
      ExpiresAt *time.Time
  }
  var ErrNotFound = errors.New("paste not found")
  type Repo struct{ /* unexported *sql.DB field */ }
  func NewRepo(db *sql.DB) *Repo
  func (r *Repo) GetPaste(ctx context.Context, id string) (*PasteMeta, error)
  func (r *Repo) Ping(ctx context.Context) error
  ```
  Task 5 (`handler`) depends on the `Repository` interface
  (`GetPaste(ctx, id) (*db.PasteMeta, error)`), which `*Repo` satisfies.

- [ ] **Step 1: Claude writes the failing test**

```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ah-naf/pastebin/shared/pgconn"
)

const localDSN = "postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable"

func setupRepo(t *testing.T) (*Repo, *sql.DB) {
	t.Helper()
	if err := pgconn.RunMigrations(localDSN, "../../../infra/migrations"); err != nil {
		t.Skipf("could not apply migrations against %s (is `docker compose up -d` running? does infra/.env POSTGRES_HOST_PORT match?): %v", localDSN, err)
	}
	conn, err := pgconn.Open(localDSN)
	if err != nil {
		t.Skipf("local Postgres not reachable at %s: %v", localDSN, err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewRepo(conn), conn
}

func insertRow(t *testing.T, conn *sql.DB, id string, isDeleted bool, expiresAt *time.Time) {
	t.Helper()
	_, err := conn.Exec(
		`INSERT INTO pastes (paste_id, s3_key, created_at, expires_at, size_bytes, is_deleted)
		 VALUES ($1, $1, now(), $2, 10, $3)`,
		id, expiresAt, isDeleted,
	)
	if err != nil {
		t.Fatalf("test setup: failed to insert row %q: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(`DELETE FROM pastes WHERE paste_id = $1`, id); err != nil {
			t.Errorf("cleanup: failed to delete test row %q: %v", id, err)
		}
	})
}

func TestGetPasteFindsValidRow(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-valid-" + t.Name()
	insertRow(t, conn, id, false, nil)

	meta, err := repo.GetPaste(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPaste() returned error: %v", err)
	}
	if meta.S3Key != id {
		t.Errorf("S3Key = %q, want %q", meta.S3Key, id)
	}
	if meta.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil", *meta.ExpiresAt)
	}
}

func TestGetPasteFindsRowWithFutureExpiry(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-future-" + t.Name()
	future := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	insertRow(t, conn, id, false, &future)

	meta, err := repo.GetPaste(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPaste() returned error: %v", err)
	}
	if meta.ExpiresAt == nil || !meta.ExpiresAt.Equal(future) {
		t.Errorf("ExpiresAt = %v, want %v", meta.ExpiresAt, future)
	}
}

func TestGetPasteRejectsExpiredRow(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-expired-" + t.Name()
	past := time.Now().Add(-time.Hour).Truncate(time.Second).UTC()
	insertRow(t, conn, id, false, &past)

	_, err := repo.GetPaste(context.Background(), id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPaste() on expired row: err = %v, want ErrNotFound", err)
	}
}

func TestGetPasteRejectsDeletedRow(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-deleted-" + t.Name()
	insertRow(t, conn, id, true, nil)

	_, err := repo.GetPaste(context.Background(), id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPaste() on deleted row: err = %v, want ErrNotFound", err)
	}
}

func TestGetPasteRejectsMissingRow(t *testing.T) {
	repo, _ := setupRepo(t)
	_, err := repo.GetPaste(context.Background(), "does-not-exist-"+t.Name())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPaste() on missing row: err = %v, want ErrNotFound", err)
	}
}

func TestRepoPing(t *testing.T) {
	repo, _ := setupRepo(t)
	if err := repo.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./read-service/internal/db/... -v`
Expected: FAIL to compile — `undefined: Repo` / `undefined: NewRepo` / `undefined: PasteMeta` / `undefined: ErrNotFound`.

- [ ] **Step 3: User writes `read-service/internal/db/repo.go`**

`GetPaste` runs `SELECT s3_key, expires_at FROM pastes WHERE paste_id = $1
AND is_deleted = false AND (expires_at IS NULL OR expires_at > now())`
with `db.QueryRowContext(ctx, ...).Scan(&s3Key, &expiresAt)` — scan
`expiresAt` into a `sql.NullTime` (or `*time.Time` directly, pgx supports
that), then build the `*PasteMeta`. On `sql.ErrNoRows`, return
`nil, ErrNotFound` (use `errors.Is(err, sql.ErrNoRows)` to check, and
return the package's own `ErrNotFound`, not the raw `sql.ErrNoRows` —
callers outside this package shouldn't need to know it's backed by SQL).
`Ping` is identical to Write Service's `Repo.Ping`.

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./read-service/internal/db/... -v`
Expected: PASS (all 6 test functions) — or SKIP if Postgres isn't reachable.

- [ ] **Step 5: Commit**

```bash
git add read-service/internal/db/repo.go read-service/internal/db/repo_test.go
git commit -m "feat: add read-only Postgres repo for read-service"
```

---

### Task 3: `read-service/internal/storage` — read-only Store

**Files:**
- Test: `read-service/internal/storage/store_test.go`
- Implementation (user writes this): `read-service/internal/storage/store.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (test uploads its own fixture
  object directly via a raw S3 client, matching Write Service's
  `storage.Store` test pattern of not depending on `shared/config`).
- Produces:
  ```go
  func NewStore(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error)
  func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
  func (s *Store) Ping(ctx context.Context) error
  ```
  Task 5 (`handler`) depends on the `Getter` interface
  (`Get(ctx, key) (io.ReadCloser, int64, error)`), which `*Store` satisfies.

- [ ] **Step 1: Claude writes the failing test**

```go
package storage

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Matches infra/.env.example's MinIO defaults from Phase 0.
const (
	testEndpoint  = "localhost:9000"
	testAccessKey = "pastebin_minio"
	testSecretKey = "pastebin_minio_password"
	testBucket    = "pastebin"
)

// uploadFixture puts an object directly via a raw S3 client — read-service's
// own Store never writes, so tests can't use it to set up fixtures.
func uploadFixture(t *testing.T, key string, content []byte) {
	t.Helper()
	client := s3.New(s3.Options{
		Region:       "ap-southeast-1",
		BaseEndpoint: aws.String("http://" + testEndpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
	})
	ctx := context.Background()
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testBucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	}); err != nil {
		t.Skipf("local MinIO not reachable/writable at %s (start it with `cd infra && docker compose up -d`): %v", testEndpoint, err)
	}
	t.Cleanup(func() {
		client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(testBucket), Key: aws.String(key)})
	})
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(context.Background(), testEndpoint, testAccessKey, testSecretKey, testBucket, false)
	if err != nil {
		t.Skipf("local MinIO not reachable at %s: %v", testEndpoint, err)
	}
	return store
}

func TestGetFetchesUploadedObject(t *testing.T) {
	key := "test-get-" + t.Name()
	content := []byte("hello from the read-service test suite")
	uploadFixture(t, key, content)

	store := newTestStore(t)
	body, size, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}
	defer body.Close()

	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body returned error: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestGetMissingKeyReturnsError(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.Get(context.Background(), "does-not-exist-"+t.Name())
	if err == nil {
		t.Error("Get() on missing key: expected error, got nil")
	}
}

func TestStorePing(t *testing.T) {
	store := newTestStore(t)
	if err := store.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./read-service/internal/storage/... -v`
Expected: FAIL to compile — `undefined: NewStore` / `undefined: Store`.

- [ ] **Step 3: User writes `read-service/internal/storage/store.go`**

Same S3 client construction as Write Service's `storage.NewStore`
(static credentials, `BaseEndpoint` + `UsePathStyle: true`), but **no**
`CreateBucket` call — this service only reads. `Get` calls
`client.GetObject(ctx, &s3.GetObjectInput{Bucket: ..., Key: key})`, returns
`(result.Body, *result.ContentLength, nil)` on success, or the error
directly on failure (a missing key surfaces as an S3 `NoSuchKey` error,
which is a real error here — the handler already knows the key is valid
from the DB lookup, so a missing object at this point is a genuine
inconsistency, not an expected case to special-case). `Ping` calls
`client.HeadBucket` same as Write Service's.

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./read-service/internal/storage/... -v`
Expected: PASS (all 3 test functions) — or SKIP if MinIO isn't reachable.

- [ ] **Step 5: Commit**

```bash
git add read-service/internal/storage/store.go read-service/internal/storage/store_test.go
git commit -m "feat: add read-only S3/MinIO storage client for read-service"
```

---

### Task 4: `read-service/internal/cache` — cache-aside with negative caching

**Files:**
- Test: `read-service/internal/cache/cache_test.go`
- Implementation (user writes this): `read-service/internal/cache/cache.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (test connects its own `redis.Client`).
- Produces:
  ```go
  type Result int
  const (
      Miss Result = iota
      Hit
      Negative
  )
  type Cache struct{ /* unexported *redis.Client field */ }
  func NewCache(client *redis.Client) *Cache
  func (c *Cache) Get(ctx context.Context, id string) ([]byte, Result)
  func (c *Cache) SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration)
  func (c *Cache) SetNegative(ctx context.Context, id string, ttl time.Duration)
  ```
  Task 5 (`handler`) depends on `CacheGetter`/`CacheSetter` interfaces built
  from these exact signatures, which `*Cache` satisfies.

- [ ] **Step 1: Claude writes the failing test**

```go
package cache

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func requireRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("local Redis not reachable at localhost:6379 (start it with `cd infra && docker compose up -d`): %v", err)
	}
	return client
}

func cleanupKeys(t *testing.T, client *redis.Client, id string) {
	t.Helper()
	t.Cleanup(func() {
		client.Del(context.Background(), "paste:content:"+id, "paste:missing:"+id)
	})
}

func TestGetOnUnsetKeyIsMiss(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-miss-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	_, result := c.Get(context.Background(), id)
	if result != Miss {
		t.Errorf("Get() on unset key = %v, want Miss", result)
	}
}

func TestSetPositiveThenGetRoundTrips(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-positive-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	content := []byte("cached content")
	c.SetPositive(context.Background(), id, content, time.Minute)

	got, result := c.Get(context.Background(), id)
	if result != Hit {
		t.Fatalf("Get() after SetPositive = %v, want Hit", result)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestSetNegativeThenGetReturnsNegative(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-negative-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	c.SetNegative(context.Background(), id, time.Minute)

	_, result := c.Get(context.Background(), id)
	if result != Negative {
		t.Errorf("Get() after SetNegative = %v, want Negative", result)
	}
}

func TestPositiveTTLExpires(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-ttl-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	c.SetPositive(context.Background(), id, []byte("x"), 50*time.Millisecond)
	time.Sleep(150 * time.Millisecond)

	_, result := c.Get(context.Background(), id)
	if result != Miss {
		t.Errorf("Get() after TTL expiry = %v, want Miss", result)
	}
}

func TestCacheDegradesGracefullyWhenRedisUnavailable(t *testing.T) {
	client := requireRedis(t)
	id := "test-degrade-" + t.Name()
	client.Close() // simulate Redis being unreachable for every call below

	c := NewCache(client)

	// None of these must panic or block despite the closed client.
	if _, result := c.Get(context.Background(), id); result != Miss {
		t.Errorf("Get() with closed client = %v, want Miss (fail open)", result)
	}
	c.SetPositive(context.Background(), id, []byte("x"), time.Minute)
	c.SetNegative(context.Background(), id, time.Minute)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./read-service/internal/cache/... -v`
Expected: FAIL to compile — `undefined: NewCache` / `undefined: Cache` / `undefined: Miss` / `undefined: Hit` / `undefined: Negative`.

- [ ] **Step 3: User writes `read-service/internal/cache/cache.go`**

Key naming: `"paste:content:" + id` for positive entries, `"paste:missing:"
+ id` for negative entries (both from the Global Constraints/design doc).
`Get`: try `client.Get(ctx, "paste:content:"+id).Bytes()` first — on
success, return `(bytes, Hit)`. On `redis.Nil` (key doesn't exist) or any
other error, try `client.Get(ctx, "paste:missing:"+id)` — if that key
exists (regardless of its value), return `(nil, Negative)`. If neither key
exists, or any Redis error occurs at any point, return `(nil, Miss)` — log
the error with the stdlib `log` package but do not return it.
`SetPositive`/`SetNegative`: call `client.Set(ctx, key, value, ttl)`,
log-and-ignore any error (no return value on these methods at all, so
there's nothing to propagate even if you wanted to).

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./read-service/internal/cache/... -v`
Expected: PASS (all 5 test functions) — or SKIP if Redis isn't reachable.

- [ ] **Step 5: Commit**

```bash
git add read-service/internal/cache/cache.go read-service/internal/cache/cache_test.go
git commit -m "feat: add cache-aside layer with negative caching for read-service"
```

---

### Task 5: `read-service/internal/handler`

**Files:**
- Test: `read-service/internal/handler/handler_test.go`
- Implementation (user writes this): `read-service/internal/handler/handler.go`

**Interfaces:**
- Consumes: `db.PasteMeta`, `db.ErrNotFound` (Task 2, type/value only —
  tests use a fake `Repository`); `cache.Result`, `cache.Miss`, `cache.Hit`,
  `cache.Negative` (Task 4, the enum values — tests use fake
  `CacheGetter`/`CacheSetter`, not the real `*cache.Cache`).
- Produces:
  ```go
  type CacheGetter interface { Get(ctx context.Context, id string) ([]byte, cache.Result) }
  type CacheSetter interface {
      SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration)
      SetNegative(ctx context.Context, id string, ttl time.Duration)
  }
  type Repository interface { GetPaste(ctx context.Context, id string) (*db.PasteMeta, error) }
  type Getter interface { Get(ctx context.Context, key string) (io.ReadCloser, int64, error) }
  type Pinger interface { Ping(ctx context.Context) error }
  type Handler struct{ /* unexported fields */ }
  func New(cacheGet CacheGetter, cacheSet CacheSetter, repo Repository, store Getter) *Handler
  func (h *Handler) GetPaste(w http.ResponseWriter, r *http.Request)
  func Healthz(postgres Pinger, s3 Pinger) http.HandlerFunc
  ```
  `*cache.Cache` satisfies both `CacheGetter` and `CacheSetter`; `*db.Repo`
  satisfies `Repository` and `Pinger`; `*storage.Store` satisfies `Getter`
  and `Pinger`. Task 6 (`main.go`) wires the real implementations in and
  registers both handler functions, extracting `{id}` via
  `r.PathValue("id")` (Go 1.22+ `ServeMux` pattern:
  `"GET /paste/{id}"`).

- [ ] **Step 1: Claude writes the failing test**

```go
package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ah-naf/pastebin/read-service/internal/cache"
	"github.com/ah-naf/pastebin/read-service/internal/db"
)

type fakeCacheGetter struct {
	content []byte
	result  cache.Result
}

func (f *fakeCacheGetter) Get(ctx context.Context, id string) ([]byte, cache.Result) {
	return f.content, f.result
}

type fakeCacheSetter struct {
	positiveCalled bool
	positiveTTL    time.Duration
	negativeCalled bool
}

func (f *fakeCacheSetter) SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration) {
	f.positiveCalled = true
	f.positiveTTL = ttl
}

func (f *fakeCacheSetter) SetNegative(ctx context.Context, id string, ttl time.Duration) {
	f.negativeCalled = true
}

type fakeRepo struct {
	meta *db.PasteMeta
	err  error
}

func (f *fakeRepo) GetPaste(ctx context.Context, id string) (*db.PasteMeta, error) {
	return f.meta, f.err
}

type fakeStore struct {
	content string
	err     error
	called  bool
}

func (f *fakeStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	f.called = true
	if f.err != nil {
		return nil, 0, f.err
	}
	return io.NopCloser(strings.NewReader(f.content)), int64(len(f.content)), nil
}

func doGetPaste(h *Handler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/paste/"+id, nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.GetPaste(rec, req)
	return rec
}

func TestGetPasteCacheHitSkipsRepoAndStore(t *testing.T) {
	cacheGet := &fakeCacheGetter{content: []byte("cached content"), result: cache.Hit}
	repo := &fakeRepo{err: errors.New("repo must not be called on cache hit")}
	store := &fakeStore{err: errors.New("store must not be called on cache hit")}
	h := New(cacheGet, &fakeCacheSetter{}, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "cached content" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "cached content")
	}
	if store.called {
		t.Error("store.Get was called despite cache hit")
	}
}

func TestGetPasteCacheNegativeReturns404WithoutRepoOrStore(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Negative}
	repo := &fakeRepo{err: errors.New("repo must not be called on negative cache hit")}
	store := &fakeStore{err: errors.New("store must not be called on negative cache hit")}
	h := New(cacheGet, &fakeCacheSetter{}, repo, store)

	rec := doGetPaste(h, "missing123")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if store.called {
		t.Error("store.Get was called despite negative cache hit")
	}
}

func TestGetPasteCacheMissRepoNotFoundSetsNegative(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{err: db.ErrNotFound}
	h := New(cacheGet, cacheSet, repo, &fakeStore{})

	rec := doGetPaste(h, "gone123")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !cacheSet.negativeCalled {
		t.Error("cache.SetNegative was not called after repo returned ErrNotFound")
	}
}

func TestGetPasteCacheMissRepoErrorReturns500(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{err: errors.New("db down")}
	h := New(cacheGet, cacheSet, repo, &fakeStore{})

	rec := doGetPaste(h, "x")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if cacheSet.negativeCalled {
		t.Error("cache.SetNegative was called for a real DB error, not just ErrNotFound")
	}
}

func TestGetPasteCacheMissRepoOKStoreErrorReturns500(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	repo := &fakeRepo{meta: &db.PasteMeta{S3Key: "abc123"}}
	store := &fakeStore{err: errors.New("s3 down")}
	h := New(cacheGet, &fakeCacheSetter{}, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestGetPasteFullMissPopulatesCache(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	cacheSet := &fakeCacheSetter{}
	expires := time.Now().Add(30 * time.Minute)
	repo := &fakeRepo{meta: &db.PasteMeta{S3Key: "abc123", ExpiresAt: &expires}}
	store := &fakeStore{content: "hello from S3"}
	h := New(cacheGet, cacheSet, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "hello from S3" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello from S3")
	}
	if !cacheSet.positiveCalled {
		t.Fatal("cache.SetPositive was not called after a full cache miss")
	}
	if cacheSet.positiveTTL <= 0 || cacheSet.positiveTTL > time.Hour {
		t.Errorf("positive TTL = %v, want > 0 and <= 1h (bounded by expires_at)", cacheSet.positiveTTL)
	}
}

func TestGetPasteContentTypeIsPlainText(t *testing.T) {
	cacheGet := &fakeCacheGetter{content: []byte("x"), result: cache.Hit}
	h := New(cacheGet, &fakeCacheSetter{}, &fakeRepo{}, &fakeStore{})

	rec := doGetPaste(h, "x")

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want prefix \"text/plain\"", ct)
	}
}

type fakePinger struct{ err error }

func (f *fakePinger) Ping(ctx context.Context) error { return f.err }

func TestHealthzBothUp(t *testing.T) {
	handlerFunc := Healthz(&fakePinger{}, &fakePinger{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handlerFunc(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHealthzPostgresDown(t *testing.T) {
	handlerFunc := Healthz(&fakePinger{err: errors.New("down")}, &fakePinger{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handlerFunc(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./read-service/internal/handler/... -v`
Expected: FAIL to compile — `undefined: New` / `undefined: Handler` / `undefined: Healthz`.

- [ ] **Step 3: User writes `read-service/internal/handler/handler.go`**

`GetPaste`: extract `id := r.PathValue("id")`. Call `h.cacheGet.Get(ctx,
id)`. On `cache.Hit`: set `Content-Type: text/plain; charset=utf-8`,
write `200`, write the cached bytes, return — never touch repo/store. On
`cache.Negative`: `http.Error(w, "paste not found", http.StatusNotFound)`,
return. On `cache.Miss`: call `h.repo.GetPaste(ctx, id)`; on
`errors.Is(err, db.ErrNotFound)`, call
`h.cacheSet.SetNegative(ctx, id, 60*time.Second)` then respond `404`; on
any other error, respond `500` (no `SetNegative` call — that's only for
confirmed not-found, not for "the DB is broken"); on success, call
`h.store.Get(ctx, meta.S3Key)` — error → `500`; success → set
`Content-Type`/`Content-Length` headers, write `200`, then `io.Copy` the
body through an `io.TeeReader` into a `bytes.Buffer` so the response and
the cache population read the same bytes once; after the copy, compute
`ttl := time.Hour` and if `meta.ExpiresAt != nil` and
`time.Until(*meta.ExpiresAt) < ttl` and positive, use that instead; call
`h.cacheSet.SetPositive(ctx, id, buf.Bytes(), ttl)`.

`Healthz(postgres, s3 Pinger) http.HandlerFunc`: identical shape to Write
Service's — both `Ping` calls succeed → `200 {"status":"ok"}`; either
fails → `503 {"status":"degraded","postgres":"ok|error","s3":"ok|error"}`.

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./read-service/internal/handler/... -v`
Expected: PASS (all 9 test functions).

- [ ] **Step 5: Commit**

```bash
git add read-service/internal/handler/handler.go read-service/internal/handler/handler_test.go
git commit -m "feat: add read-service HTTP handlers"
```

---

### Task 6: `read-service/cmd/read-service/main.go`

No Go test file — wiring, verified end-to-end against a paste actually
created by Write Service, proving the two independently-built services
interoperate.

**Files:**
- Implementation (user writes this): `read-service/cmd/read-service/main.go`

**Interfaces:**
- Consumes: `config.Load()` (Task 1's now-optional `ID_XOR_SECRET`),
  `pgconn.Open` (Phase 1), `cache.NewClient` (Phase 2's
  `shared/cache`), `db.NewRepo`/`storage.NewStore`/`cache.NewCache`/
  `handler.New`/`handler.Healthz` (Tasks 2-5).
- Produces: a running HTTP server on `cfg.Port` with `GET /paste/{id}` and
  `GET /healthz` registered. **No `POST /paste` route exists in this
  binary** — that's the whole point of Hard Constraint #1.

- [ ] **Step 1: User writes `read-service/cmd/read-service/main.go`**

Mirrors `write-service/cmd/write-service/main.go`'s structure: `config.Load()`
(fatal on error — no `ID_XOR_SECRET` check needed here, unlike write-service)
→ `pgconn.Open(cfg.DatabaseURL)` (fatal on error) → apply the same bounded
pool settings (`SetMaxOpenConns`/`SetMaxIdleConns`/`SetConnMaxLifetime`
from `cfg`) → `cache.NewClient(cfg.RedisAddr)` →
`readcache.NewCache(redisClient)` (Task 4's package; alias the import if it
collides with `shared/cache`, e.g. `import readcache
"github.com/ah-naf/pastebin/read-service/internal/cache"`) →
`storage.NewStore(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey,
cfg.S3Bucket, cfg.S3UseSSL)` (fatal on error — no bucket auto-create call,
per Task 3) → `db.NewRepo(sqlDB)` →
`handler.New(readcache, readcache, repo, store)` (the same `*cache.Cache`
value satisfies both the getter and setter interfaces). Register
`mux.HandleFunc("GET /paste/{id}", h.GetPaste)` and
`mux.HandleFunc("GET /healthz", handler.Healthz(repo, store))`. Same
graceful-shutdown pattern as write-service's `main.go`
(`signal.NotifyContext` + `server.Shutdown`) — copy that structure rather
than inventing a new one.

- [ ] **Step 2: Build and start both services, verify end-to-end together**

Terminal 1 (Write Service, same as Phase 2's verification):

```bash
cd infra && docker compose up -d && cd ..
DATABASE_URL="postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable" \
S3_ACCESS_KEY=pastebin_minio S3_SECRET_KEY=pastebin_minio_password \
ID_XOR_SECRET=9f3a1c2e5b7d0f14 \
go run ./write-service/cmd/write-service
```

Terminal 2 (Read Service — note: no `ID_XOR_SECRET`, no `PORT` override
needed since it defaults to `8081`, which is what Write Service's
`PUBLIC_BASE_URL` default already points at):

```bash
DATABASE_URL="postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable" \
S3_ACCESS_KEY=pastebin_minio S3_SECRET_KEY=pastebin_minio_password \
PORT=8081 \
go run ./read-service/cmd/read-service
```

Terminal 3:

```bash
curl -i http://localhost:8081/healthz

# Create a paste via Write Service, capture its id from the JSON response
curl -s -X POST http://localhost:8080/paste -d '{"content":"end to end works"}'

# Fetch it back via Read Service using that id
curl -i http://localhost:8081/paste/<id-from-above>

# Confirm the SECOND fetch is a cache hit (same content, and — informally —
# noticeably faster / no S3 log line if you're watching one)
curl -i http://localhost:8081/paste/<id-from-above>

# A made-up id must 404, twice (second call proves negative caching doesn't
# error, even though you can't observe the DB-skip from curl alone)
curl -i http://localhost:8081/paste/definitely-not-a-real-id
curl -i http://localhost:8081/paste/definitely-not-a-real-id
```

Expected: `/healthz` → `200`. First `POST /paste` → `201` with an `id`.
Both `GET /paste/<id>` calls → `200` with body `"end to end works"` and
`Content-Type: text/plain; charset=utf-8`. Both made-up-id calls → `404`.

- [ ] **Step 3: Commit**

```bash
git add read-service/cmd/read-service/main.go
git commit -m "feat: wire up read-service main"
```

---

## Phase 3 done-criteria checklist

- [ ] `go test ./...` passes (Redis/Postgres/MinIO-dependent tests pass or skip cleanly).
- [ ] `go run ./read-service/cmd/read-service` starts and serves `GET /healthz` → `200`.
- [ ] A paste created via Write Service's `POST /paste` is retrievable via Read Service's `GET /paste/{id}` — verified by hand in Task 6.
- [ ] A cache hit skips the DB and S3 entirely (proven by the handler unit tests, not observable via curl alone).
- [ ] A missing/expired/deleted paste ID returns `404` from Read Service, both before and after negative caching kicks in.
- [ ] Read Service has no `POST /paste` route — confirmed by `main.go`'s route registration containing only `GET` handlers.
- [ ] Killing Redis (`docker compose stop redis`) does not break `GET /paste/{id}` for a previously-cached-or-not id — it just gets slower (falls through to DB+S3 every time). Not scripted in this plan; worth trying by hand if you want to see the "cache is never a hard dependency" constraint hold under an actual outage.

Once checked, Phase 3 is done. Next: Phase 4 (Expiration sweeper — a
separate background process, not part of Read or Write Service, that
deletes expired metadata rows and their S3 objects on a schedule).
