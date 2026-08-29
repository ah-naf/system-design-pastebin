# Phase 3 Design — Read Service

## Context

Phase 0-2 are complete: repo/Docker stack, `shared/id`, and Write Service
(`POST /paste`, `GET /healthz`). This is Phase 3 per the governing project
brief: the Read Service, exposing `GET /paste/{id}`, following Hard
Constraint #3 (read path is always cache → metadata DB → S3, cache-aside
population on miss, negative caching for missing/expired) and Hard
Constraint #1 (Read Service never accepts writes — no `POST /paste` here,
that's Write Service's job only). It also enforces the project's
non-functional bar that cache is never a hard dependency: every cache
operation swallows its own errors internally, so a dead Redis degrades
performance, not correctness.

## Goals

- `GET /paste/{id}`: cache check → DB lookup (with expiry/deletion filter
  already applied in the query) → S3 fetch → populate cache → stream
  content to the client.
- `GET /healthz`: readiness check (Postgres + S3 — not Redis, since Redis
  being down must never fail readiness).
- Negative caching: a 404 result gets cached too, so repeated requests for
  a missing/expired/deleted ID skip the DB entirely for a short window.
- Same interface-seam pattern as Write Service: handler depends on
  interfaces, unit-testable with fakes; only `cache`/`db`/`storage` have
  real-dependency integration tests.

## Components

```
read-service/internal/db/repo.go
    type PasteMeta struct { S3Key string; ExpiresAt *time.Time }
    var ErrNotFound = errors.New("paste not found")
    type Repo struct{ db *sql.DB }
    func NewRepo(db *sql.DB) *Repo
    func (r *Repo) GetPaste(ctx context.Context, id string) (*PasteMeta, error)
        — single query: SELECT s3_key, expires_at FROM pastes
          WHERE paste_id = $1 AND is_deleted = false
            AND (expires_at IS NULL OR expires_at > now())
          Returns ErrNotFound on sql.ErrNoRows (covers "never existed",
          "deleted", and "expired" identically — the client can't tell
          these apart anyway, and neither does the cache).
    func (r *Repo) Ping(ctx context.Context) error

read-service/internal/storage/store.go
    type Store struct{ client *s3.Client; bucket string }
    func NewStore(ctx, s3Endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error)
        — no bucket auto-create here (unlike Write Service): Read Service
          only ever reads, and a missing bucket is Write Service's setup
          problem, not something a read-only service should have
          CreateBucket permission to fix.
    func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
        — the int64 is content length (from the S3 GetObject response),
          needed for the Content-Length header.
    func (s *Store) Ping(ctx context.Context) error

read-service/internal/cache/cache.go
    type Result int
    const (
        Miss Result = iota  // not in cache at all — go to DB
        Hit                  // positive cache hit — content is valid
        Negative             // known-missing — skip DB, return 404 now
    )
    type Cache struct{ client *redis.Client }
    func NewCache(client *redis.Client) *Cache
    func (c *Cache) Get(ctx context.Context, id string) ([]byte, Result)
        — checks "paste:content:{id}" first, then "paste:missing:{id}".
          A Redis error at any point is logged and treated as Miss — the
          handler falls through to the DB exactly as if nothing were
          cached. This is the one place the "cache is never a hard
          dependency" rule is enforced structurally: this method has no
          error return, so a caller cannot accidentally treat a cache
          failure as a request failure.
    func (c *Cache) SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration)
    func (c *Cache) SetNegative(ctx context.Context, id string, ttl time.Duration)
        — both fire-and-forget: log a Redis error, don't return one.

read-service/internal/handler/handler.go
    type CacheGetter interface { Get(ctx context.Context, id string) ([]byte, cache.Result) }
    type CacheSetter interface {
        SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration)
        SetNegative(ctx context.Context, id string, ttl time.Duration)
    }
    type Repository interface { GetPaste(ctx context.Context, id string) (*db.PasteMeta, error) }
    type Getter interface { Get(ctx context.Context, key string) (io.ReadCloser, int64, error) }
    type Pinger interface { Ping(ctx context.Context) error }
    type Handler struct{ cache CacheGetter; cacheSet CacheSetter; repo Repository; store Getter }
    func New(cache CacheGetter, cacheSet CacheSetter, repo Repository, store Getter) *Handler
    func (h *Handler) GetPaste(w http.ResponseWriter, r *http.Request)  // GET /paste/{id}
    func Healthz(postgres Pinger, s3 Pinger) http.HandlerFunc  // identical shape to Write Service's

read-service/cmd/read-service/main.go
    Loads config (reuses shared/config.Load — Read Service needs
    DatabaseURL, RedisAddr, S3Endpoint/credentials/bucket, Port; it does
    NOT need ID_XOR_SECRET or MAX_PASTE_BYTES, but Load() currently
    requires ID_XOR_SECRET unconditionally — see Open Question below),
    opens DB (with the same bounded pool settings as Write Service), opens
    Redis, builds Store/Repo/Cache/Handler, registers GET /paste/{id} and
    GET /healthz, graceful shutdown identical to Write Service's.
```

## Data Flow

**`GET /paste/{id}`:**

1. `cache.Get(ctx, id)`:
   - `Hit` → write the cached bytes directly to the response with
     `Content-Type: text/plain; charset=utf-8` and `200 OK`. Done — no DB,
     no S3.
   - `Negative` → `404 Not Found`. Done — no DB, no S3.
   - `Miss` → continue to step 2.
2. `repo.GetPaste(ctx, id)`:
   - `ErrNotFound` → `cache.SetNegative(ctx, id, 60*time.Second)`, respond
     `404 Not Found`.
   - other error → `500 Internal Server Error` (DB is a hard dependency;
     this is the one failure mode that legitimately fails the request).
   - success → continue to step 3 with `meta.S3Key`.
3. `store.Get(ctx, meta.S3Key)` → `(body io.ReadCloser, size int64, error)`.
   - error → `500 Internal Server Error` (S3 is also a hard dependency for
     reads — there's no fallback content source).
   - success → continue to step 4.
4. Set `Content-Type: text/plain; charset=utf-8` and
   `Content-Length: size`, write `200 OK`, then `io.Copy` the S3 body into
   both the `http.ResponseWriter` and an in-memory buffer simultaneously
   via `io.TeeReader(body, &buf)` (client gets the bytes as they stream;
   `buf` ends up holding the full content once `io.Copy` finishes).
5. After the copy succeeds, compute the positive-cache TTL: `1*time.Hour`,
   or `time.Until(*meta.ExpiresAt)` if that's sooner and still positive.
   Call `cache.SetPositive(ctx, id, buf.Bytes(), ttl)`. This runs after the
   response is already fully sent, so cache population never adds latency
   to the client-visible request — and per the `Cache` design, any Redis
   failure here is swallowed, not surfaced.

**`GET /healthz`:** identical shape to Write Service's — pings Postgres and
S3 with a short timeout, `200 {"status":"ok"}` or
`503 {"status":"degraded","postgres":"ok|error","s3":"ok|error"}`. Redis is
deliberately not part of this check.

## Error Handling

- **Cache failures never fail a request.** Every `Cache` method swallows
  its own Redis errors (log, then behave as `Miss` / do nothing). The
  handler code has no `if err != nil` branch for cache calls at all — the
  types don't expose one. This is the concrete mechanism behind "cache
  must never be a hard dependency."
- **DB and S3 failures do fail the request** (`500`) — unlike cache, there
  is no fallback path for "the metadata database is unreachable" or "S3 is
  down." That's a real outage, not a cache miss, and the client should see
  a server error rather than a false 404.
- **A negative cache entry from one moment doesn't block a paste created
  a moment later for long** — the 60-second TTL bounds how long a genuine
  race (client requests an ID moments before Write Service's insert
  commits, though this shouldn't happen in practice since Write Service
  returns 201 only after the DB commit) could show a stale 404.

## Open Question (resolved below, not deferred)

`shared/config.Load()` currently requires `ID_XOR_SECRET`, which only
Write Service uses (for `id.Generator`). Read Service has no use for it.
Rather than fork a second config loader (duplicated env-var parsing logic,
a real DRY violation for two services that share every other setting),
`ID_XOR_SECRET` becomes optional in `shared/config`: if unset, `Load()`
leaves `Config.IDXORSecret` as its zero value and does not error. Write
Service's own startup is unaffected (it still requires the var — that
requirement moves from `shared/config.Load` into a startup-time check
inside `write-service/cmd/write-service/main.go`, since only that binary
actually needs it). This is a small, targeted change to already-tested
code — Task 1 of the implementation plan.

## Testing Approach

Same pattern as Write Service:

- **`handler` tests** (pure, no real Postgres/S3/Redis): fake
  `CacheGetter`/`CacheSetter`/`Repository`/`Getter` — cache hit returns
  content immediately (repo/store never called), cache negative returns
  404 immediately (repo/store never called), cache miss + repo not-found
  → 404 + `SetNegative` called, cache miss + repo error → 500, cache miss +
  repo success + store error → 500, full miss-then-success path → 200 with
  correct body/headers + `SetPositive` called with the right TTL.
- **`cache.Cache` tests**: integration, real Redis (skip if unreachable) —
  positive set-then-get round-trips, negative set-then-get returns
  `Negative`, an unset key returns `Miss`, TTL expiry (short TTL in the
  test, sleep past it, confirm it reverts to `Miss`).
  cache degradation is also tested here directly: closing the Redis client
  before calling `Get`/`SetPositive`/`SetNegative` must not panic and must
  behave as `Miss`/no-op (this is the "never a hard dependency" contract,
  tested at the unit that owns it).
- **`db.Repo` tests**: integration, real Postgres (skip if unreachable) —
  a row inserted directly via SQL (not through Write Service) with no
  expiry is found; one with `is_deleted = true` returns `ErrNotFound`; one
  with `expires_at` in the past returns `ErrNotFound`; one with
  `expires_at` in the future is found.
- **`storage.Store` tests**: integration, real MinIO (skip if unreachable)
  — an object uploaded directly (not through Write Service) via the test's
  own S3 client is fetched correctly with the right size; fetching a
  missing key returns an error.
- **End-to-end** (plan's final task, manual): start Write Service, `POST` a
  paste, start Read Service, `GET` it back — confirms the two independently
  built services actually interoperate, which no unit test can prove.

## Out of Scope for This Document

Cache stampede protection (concurrent misses all hitting the DB/S3 at
once) is Phase 5. Metrics/observability is Phase 6. The load balancer in
front of Read Service is Phase 6. Deleting expired rows/objects is Phase
4's sweeper — Read Service only filters expired rows out of query results,
it never deletes them.
