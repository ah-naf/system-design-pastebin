# Phase 4 Design — Expiration Sweeper

## Context

Phases 0-3 are complete: Write Service and Read Service are both running.
Neither one deletes anything — Write Service only ever sets `expires_at`,
and Read Service's query just filters expired rows out of results without
touching them. This is Phase 4 per the governing project brief: a
background sweeper, run as a **separate process**, not embedded in either
service, that physically deletes expired metadata rows and their S3
objects.

## Goals

- Find rows where `expires_at` has passed and `is_deleted = false`, delete
  the Postgres row, then best-effort delete the matching S3 object.
- Process in bounded batches, not one unbounded query, so a large backlog
  (sweeper hasn't run in a while) doesn't do one giant lock/scan.
- Run once per invocation and exit — scheduling (how often it runs) is an
  external concern (cron, a k8s `CronJob`, or a local loop script for this
  project), not application code.
- Same interface-seam pattern as the other services: the sweep logic
  itself depends on interfaces, unit-tested with fakes; only the DB/S3
  implementations get real-dependency integration tests.

## Components

```
sweeper/internal/db/repo.go
    type ExpiredPaste struct { ID string; S3Key string }
    type Repo struct{ db *sql.DB }
    func NewRepo(db *sql.DB) *Repo
    func (r *Repo) FindExpiredBatch(ctx context.Context, limit int) ([]ExpiredPaste, error)
        — SELECT paste_id, s3_key FROM pastes
           WHERE expires_at IS NOT NULL AND expires_at <= now()
             AND is_deleted = false
           ORDER BY expires_at ASC
           LIMIT $1
          (oldest-expired first — no functional requirement for this order,
          just deterministic and reasonable: the longest-overdue rows
          clear first if a run gets interrupted partway through)
    func (r *Repo) DeleteMetadata(ctx context.Context, id string) error
        — DELETE FROM pastes WHERE paste_id = $1 (hard delete — is_deleted
          is never actually set to true by any code path yet, so this
          isn't a soft-delete cleanup job, it's the only deletion path
          that exists)

sweeper/internal/storage/store.go
    type Store struct{ client *s3.Client; bucket string }
    func NewStore(ctx, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error)
        — no bucket auto-create, same reasoning as Read Service: this
          binary only ever deletes, it shouldn't need CreateBucket
          permission either.
    func (s *Store) Delete(ctx context.Context, key string) error

sweeper/internal/sweep/sweep.go
    type Repository interface {
        FindExpiredBatch(ctx context.Context, limit int) ([]db.ExpiredPaste, error)
        DeleteMetadata(ctx context.Context, id string) error
    }
    type Deleter interface { Delete(ctx context.Context, key string) error }
    func Run(ctx context.Context, repo Repository, store Deleter, batchSize int) (deleted int, err error)
        — the actual sweep loop, independent of main.go so it's testable
          with fakes. See Data Flow below for exact per-row behavior.

sweeper/cmd/sweeper/main.go
    Loads config (reuses shared/config.Load() — DatabaseURL and S3
    fields are the only ones this binary needs; Port/PublicBaseURL/
    RedisAddr/MaxPasteBytes/IDXORSecret are all simply unused), opens DB
    with the same bounded pool settings as the other services, builds
    Store/Repo, calls sweep.Run under an overall 5-minute timeout context,
    logs a summary (rows deleted), exits 0. No migrations run here — the
    sweeper assumes Write Service has already applied them. No HTTP
    server, no `/healthz` — this is a one-shot CLI, not a long-running
    service, so there's nothing to probe.
```

## Data Flow

**One invocation of `sweep.Run`:**

1. Call `repo.FindExpiredBatch(ctx, batchSize)`.
2. If the batch is empty, stop — return the running total deleted so far.
3. For each row in the batch, in order:
   - `repo.DeleteMetadata(ctx, row.ID)`. If this errors, log it and
     **skip the S3 delete for this row** — leave the S3 object in place
     and move to the next row. The metadata row is still there too (the
     delete failed), so this row will simply be picked up again on the
     next sweeper run. No partial state is created.
   - If `DeleteMetadata` succeeded, call `store.Delete(ctx, row.S3Key)`.
     If this errors, log it and move on anyway — the metadata row is
     already gone (the row is now correctly invisible to Read Service,
     which was already true before physical deletion since its query
     filters expired rows), so an S3 delete failure here just means a
     leftover object, the same "orphaned object, not orphaned metadata"
     tradeoff Write Service already accepts.
   - Either way, increment the running deleted count only when
     `DeleteMetadata` succeeded (an S3-delete failure doesn't undo
     counting the row as swept — the metadata is gone, which is the
     primary cleanup goal).
4. Go back to step 1 (fetch the next batch) — repeat until a batch comes
   back empty.

**`sweeper/cmd/sweeper/main.go`:** wraps the whole `sweep.Run` call in a
`context.WithTimeout(ctx, 5*time.Minute)` — if the DB or S3 hangs, the
process exits with an error after 5 minutes rather than hanging forever
(important for a cron-invoked tool: a stuck process piling up on every
scheduled tick is worse than one that fails fast).

## Error Handling

- **A `DeleteMetadata` failure is retried automatically, for free** — the
  row stays in the table, so the next scheduled run finds it again in its
  next `FindExpiredBatch` call. No special retry logic needed in this
  binary.
- **A `store.Delete` failure is never retried** — once the metadata row is
  gone, nothing in this system will ever look at that S3 key again by ID
  (Read Service can't reach it — the DB row that would resolve `id →
  s3_key` no longer exists). This is an accepted permanent leak in the
  rare case of an S3 failure at exactly that moment; reconciling truly
  orphaned S3 objects (content with no DB row, from either this failure
  mode or Write Service's own accepted DB-insert-failure tradeoff) is a
  separate maintenance concern this project doesn't build a tool for.
- **`FindExpiredBatch` failing** (e.g. DB unreachable) stops the whole run
  immediately — `sweep.Run` returns that error, `main.go` logs it and
  exits non-zero. A cron/CronJob scheduler sees the failed exit code and
  retries on its own next scheduled tick; this binary does not retry
  within a single invocation.

## Testing Approach

- **`sweep.Run` tests** (pure, no real Postgres/S3): fake `Repository`
  and `Deleter` — a repo that returns two batches then an empty one
  (proves the loop continues across batches and stops correctly), a
  `DeleteMetadata` failure on one row (proves that row's S3 object is
  never touched and the row isn't counted as deleted, while other rows in
  the same batch still get processed), a `store.Delete` failure (proves
  the row still counts as deleted since metadata removal succeeded), an
  immediately-empty first batch (proves zero deletions, no errors), a
  `FindExpiredBatch` error (proves the whole run stops and returns that
  error).
- **`db.Repo` tests**: integration, real Postgres (skip if unreachable) —
  seed one expired row, one future-expiry row, one already-`is_deleted`
  row, one never-expiring (`NULL`) row; `FindExpiredBatch` returns only
  the first; `LIMIT` is respected with more expired rows than the limit;
  `DeleteMetadata` actually removes the row and is idempotent-safe to call
  on an already-gone ID (no error, similar to a no-op).
- **`storage.Store` tests**: integration, real MinIO (skip if unreachable)
  — upload a fixture object directly (same pattern as Read Service's
  test), `Delete` removes it, confirmed by a follow-up `GetObject` failing;
  deleting an already-missing key does not error (S3's `DeleteObject` is
  idempotent by nature — this is a property of the API, not custom code).
- **End-to-end** (plan's final task, manual): seed real expired rows +
  matching S3 objects (via Write Service, then manually back-dating
  `expires_at` with SQL, since Write Service itself only ever sets
  future/never expiry), run the sweeper binary once, confirm the rows and
  objects are gone and untouched non-expired rows remain.

## Out of Scope for This Document

Actually configuring a real OS cron job, systemd timer, or k8s `CronJob`
manifest is deployment/ops configuration, not this project's Go code —
Phase 6 (Scaling & Ops) is where deployment configuration lives, if it's
ever built out beyond local Docker Compose. Reconciling orphaned S3
objects (content with no matching DB row, from either this phase's or
Write Service's own accepted failure tradeoffs) is not built — noted as a
known gap, not a task.
