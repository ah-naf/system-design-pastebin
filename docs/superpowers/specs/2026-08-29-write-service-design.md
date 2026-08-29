# Phase 2 Design — Write Service

## Context

Phase 0 (repo + Docker stack) and Phase 1 (`shared/id`) are complete. This
is Phase 2 per the governing project brief: the Write Service, exposing
`POST /paste`, following Hard Constraint #2 (upload to S3 first, commit
metadata second — never the reverse) and Hard Constraint #1 (Write Service
never serves reads; it's a separate deployable from Read Service, which
Phase 3 builds).

## Goals

- `POST /paste`: generate ID, upload content to S3/MinIO, write a metadata
  row to Postgres, return a shareable URL.
- `GET /healthz`: readiness check (Postgres + S3 both reachable).
- Every dependency (S3 client, DB repo, ID generator) sits behind an
  interface the HTTP handler depends on, so handler logic is unit-testable
  with fakes — no real Postgres/S3 needed for the bulk of the tests. This
  continues the pattern `shared/id` established with `CounterSource`.
- No orphaned metadata rows: a metadata row is only ever written after the
  S3 upload it references has already succeeded. This is structural (an
  ordering guarantee), not a runtime check.

## Components

```
shared/config/config.go       Load() (*Config, error) — all env vars, one place
shared/pgconn/pgconn.go       Open(databaseURL string) (*sql.DB, error) — pgx/v5 stdlib driver
shared/cache/redis.go         NewClient(addr string) *redis.Client — thin constructor

write-service/internal/storage/store.go
    type Store struct{ client *s3.Client; bucket string }
    func NewStore(ctx, s3Endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error)
        — constructs the S3 client, calls CreateBucket, ignores "already
          exists" errors (bucket auto-create per your decision)
    func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64) error
    func (s *Store) Ping(ctx context.Context) error  — HeadBucket, used by /healthz

write-service/internal/db/repo.go
    type Paste struct {
        ID         string
        S3Key      string
        CreatedAt  time.Time
        ExpiresAt  *time.Time // nil = never expires
        SizeBytes  int64
        IsDeleted  bool
        OwnerID    *string    // always nil until an auth system exists
    }
    type Repo struct{ db *sql.DB }
    func NewRepo(db *sql.DB) *Repo
    func (r *Repo) InsertPaste(ctx context.Context, p Paste) error
    func (r *Repo) Ping(ctx context.Context) error  — SELECT 1, used by /healthz

write-service/internal/handler/handler.go
    type IDGenerator interface { New() (string, error) }        // shared/id.Generator satisfies this
    type Storer interface { Put(ctx context.Context, key string, r io.Reader, size int64) error }
    type Repository interface { InsertPaste(ctx context.Context, p db.Paste) error }
    type Handler struct{ gen IDGenerator; store Storer; repo Repository; baseURL string; maxBytes int64 }
    func New(gen IDGenerator, store Storer, repo Repository, baseURL string, maxBytes int64) *Handler
    func (h *Handler) CreatePaste(w http.ResponseWriter, r *http.Request)  // POST /paste

    type Pinger interface { Ping(context.Context) error }  // db.Repo and storage.Store both satisfy this
    func Healthz(postgres Pinger, s3 Pinger) http.HandlerFunc

write-service/cmd/write-service/main.go
    Loads config, opens DB, runs migrations (golang-migrate, embedded from
    infra/migrations), opens Redis, builds shared/id.Generator with
    RedisCounterSource, builds Store (bucket auto-create), builds Repo,
    registers routes on http.ServeMux, ListenAndServe.

infra/migrations/000001_init.up.sql
infra/migrations/000001_init.down.sql
    pastes table: paste_id (text PK), s3_key (text), created_at (timestamptz),
    expires_at (timestamptz, nullable), size_bytes (bigint), is_deleted (boolean,
    default false), owner_id (text, nullable).
```

## Data Flow

**`POST /paste`** with JSON body `{"content": "...", "expires_in_seconds": 3600}`
(`expires_in_seconds` optional; omitted or `0` means never-expiring,
`expires_at` stored as `NULL`):

1. `http.MaxBytesReader` caps the body at `maxBytes` (config, default 1MiB);
   exceeding it returns `413 Payload Too Large`.
2. Decode JSON; empty/whitespace-only `content` returns `400 Bad Request`.
3. `gen.New()` generates the paste ID. Failure (e.g. Redis unreachable)
   returns `500` — no S3 call is made.
4. `store.Put(ctx, id, content, size)` uploads to S3/MinIO under key `id`.
   Failure returns `500` — no DB insert is made (Hard Constraint #2).
5. `repo.InsertPaste(ctx, paste)` writes the metadata row. Failure returns
   `500`; the S3 object from step 4 is left in place (see Error Handling).
6. `201 Created` with `{"id": "...", "url": "<baseURL>/paste/<id>", "expires_at": "...|null"}`.
   `baseURL` comes from config (`PUBLIC_BASE_URL`, default
   `http://localhost:8081` — the Read Service's default port, since that's
   what eventually serves `GET /paste/{id}`; this is a placeholder until
   Phase 6's load balancer gives every service a stable public address).

**`GET /healthz`**: calls `Ping` on both the DB repo and the S3 store with a
short timeout. Both succeed → `200 {"status":"ok"}`. Either fails →
`503 {"status":"degraded","postgres":"ok|error","s3":"ok|error"}`.

## Error Handling

- **Orphaned metadata rows are structurally impossible**: the code path
  that writes a metadata row only runs after `store.Put` has already
  returned success. There is no code path that inserts a row first.
- **Orphaned S3 objects are accepted, not cleaned up.** If step 5 (DB
  insert) fails after step 4 (S3 upload) succeeded, the S3 object stays.
  This is a deliberate scope cut: the hard constraint only forbids orphaned
  *metadata rows* (a DB row pointing at content that doesn't exist), which
  the ordering already prevents. An orphaned object (content with no DB
  row) is wasted storage, not a correctness or data-loss problem, and
  Phase 4's sweeper can later be extended to reconcile it if it matters in
  practice. Adding compensating-delete logic now would be handling a
  failure mode the spec doesn't ask for.
- **Client never sees a partial success.** Every failure path returns a
  non-2xx status; the response body never claims a paste was created
  unless both the S3 upload and the DB insert succeeded.
- **Bucket/schema setup failures are fatal at startup**, not per-request:
  if `NewStore`'s bucket-ensure call or the migration run fails, `main.go`
  logs and exits non-zero rather than starting an HTTP server that can't
  actually serve writes.

## Testing Approach

Following the `shared/id` pattern (interfaces + fakes for the bulk of
tests, a smaller set of real-dependency integration tests):

- **`handler` tests** (pure, no real Postgres/S3/Redis): table-driven tests
  against `CreatePaste` using fake `IDGenerator`/`Storer`/`Repository`
  implementations — empty content (400), oversized body (413), generator
  error (500, store never called), store error (500, repo never called),
  repo error (500, response doesn't claim success), happy path (201 with
  correct JSON shape and `expires_at` handling for both omitted and
  provided `expires_in_seconds`). Uses `net/http/httptest`.
- **`storage.Store` tests**: integration, real MinIO from the Phase 0
  Docker stack (skip cleanly if unreachable, matching `shared/id`'s
  `requireRedis` pattern) — `Put` then verify the object exists via a
  direct `GetObject`; bucket auto-create verified by using a throwaway
  bucket name and confirming `NewStore` succeeds on both a fresh and an
  already-existing bucket.
- **`db.Repo` tests**: integration, real Postgres from the Docker stack
  (same skip-if-unreachable pattern), run against a migrated schema —
  `InsertPaste` then verify the row via a direct `SELECT`; a duplicate ID
  insert is expected to fail (primary key constraint), confirming the
  schema itself enforces uniqueness as a second line of defense behind
  `shared/id`'s own uniqueness guarantee.
- **Migrations**: verified by the `db.Repo` integration tests actually
  running against a database that only has the migration applied (no
  separate migration-runner test) — if the schema is wrong, every `Repo`
  test fails immediately with a clear SQL error.

## Out of Scope for This Document

Reading pastes back (`GET /paste/{id}`) is Phase 3 (Read Service). The
load balancer that gives Write Service a stable, scalable public address
(referenced above for `PUBLIC_BASE_URL`) is Phase 6. Expiration cleanup
(deleting rows/objects once `expires_at` passes) is Phase 4's sweeper, not
this service — Write Service only ever sets `expires_at`, it never reads or
enforces it.
