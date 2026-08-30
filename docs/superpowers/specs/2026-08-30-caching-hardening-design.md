# Phase 5: Caching Hardening — Design

## Goal

Protect the read-service's DB and S3 backends from cache-stampede load when a
popular paste's cache entry expires (or a viral paste is read for the first
time) and many concurrent requests miss the cache for the same paste ID at
once. Also configure Redis eviction behavior so the cache degrades
predictably under memory pressure instead of growing unbounded or erroring.

## Scope

Two independent, small changes to the read-service and its infra:

1. **Same-key stampede protection** via a Redis distributed lock, so only
   one request per paste ID does the DB+S3 fetch on a miss; concurrent
   requests for the same ID wait briefly for that result instead of all
   hitting DB+S3 independently.
2. **Redis eviction config** (`maxmemory` + `maxmemory-policy`) for the dev
   Redis container.

General cross-key thundering-herd/backpressure protection is explicitly out
of scope for this phase.

## Hard constraints (carried over from the project-wide spec)

- Redis remains a non-critical dependency: any Redis failure (lock
  acquisition included) must degrade to today's behavior (direct DB+S3
  fetch), never a hard failure or unbounded block.
- Cache-aside read path (cache → DB → S3, with negative caching) stays as-is;
  this phase only adds coordination around the *miss* branch.

## Architecture

Lock methods are added directly to the existing `read-service/internal/cache.Cache`
type (`read-service/internal/cache/cache.go`) rather than a new type/file —
they're a natural extension of the existing cache-aside layer, and the type
is small enough that this keeps related Redis operations together.

```go
// AcquireLock attempts to acquire a short-lived per-paste lock.
// Returns true if this call acquired the lock, false if another
// holder already has it. A non-nil error means Redis itself failed
// (as opposed to normal lock contention) and callers should treat
// this the same as a cache miss with no lock available.
func (c *Cache) AcquireLock(ctx context.Context, id string) (bool, error)

// ReleaseLock releases a previously acquired lock. Safe to call
// even if the lock was never acquired or has already expired.
func (c *Cache) ReleaseLock(ctx context.Context, id string) error
```

- **Key:** `paste:lock:{id}`
- **Acquire:** `SET paste:lock:{id} 1 NX PX 5000` (5s TTL) — the short TTL
  bounds how long a crashed lock-holder can block other requests before the
  lock self-heals.
- **Release:** plain `DEL paste:lock:{id}`.
- Both operations return the underlying Redis error (unlike `Get`/
  `SetPositive`/`SetNegative`, which swallow errors) because the handler
  needs to distinguish "lock is held by someone else" from "Redis is
  broken" — these two cases lead to different fallback behavior (see below).

## Data flow (read-service handler, `GetPaste`)

Today: `Get(id)` → Miss → repo lookup → S3 fetch → populate cache → respond.

New, only on the Miss branch:

1. Call `AcquireLock(ctx, id)`.
2. **Acquired (`true, nil`):** proceed with today's existing miss path
   unchanged (repo lookup → S3 fetch → `SetPositive`/`SetNegative`),
   wrapped in `defer ReleaseLock(ctx, id)` so the lock is freed as soon as
   the fetch completes rather than held for the full 5s TTL.
3. **Redis error (`false, err`):** skip locking entirely — proceed with
   today's existing miss path directly, no `ReleaseLock` call (nothing was
   acquired). This is the "Redis is non-critical" fallback.
4. **Contended (`false, nil`):** poll `Get(ctx, id)` every 50ms, up to a
   ~1 second total budget (20 attempts):
   - `Hit` → serve the cached content directly (same as a normal cache hit).
   - `Negative` → respond 404 (same as today's negative-cache path).
   - Budget exhausted with no result → fall through to today's existing
     miss path directly (same as case 3) — this bounds the worst-case
     added latency to ~1s and guarantees no request blocks indefinitely
     even if the lock holder crashed mid-fetch (though the 5s lock TTL
     would also eventually let a new request through as the winner).

Net effect under normal conditions: exactly one request per paste ID per
miss episode touches DB/S3; every other concurrent request for that ID is
served from the cache the winner populates, typically within tens of
milliseconds. Cases 3 and the case-4 timeout preserve today's behavior as a
safety net — nothing about this change can make a request slower than
today's worst case by more than the ~1s poll budget, and nothing introduces
a hard Redis dependency.

## Interfaces & testing

The handler's existing `CacheGetter`/`CacheSetter` interfaces (in
`read-service/internal/handler/handler.go`) gain the two new methods,
following the same interface-seam pattern already used throughout the
project — fakes in unit tests, the real `*cache.Cache` in production.

Handler test cases to add:

- Lock acquired → existing miss path runs, lock released after.
- Lock acquisition returns an error → falls through to miss path directly,
  no poll loop attempted.
- Contended, then cache becomes populated (Hit) partway through polling →
  serves the polled result, never calls the repo/store.
- Contended, then cache becomes negative (Negative) partway through polling
  → responds 404, never calls the repo/store.
- Contended for the full poll budget with no result → falls through to the
  miss path directly.

The fake cache used in these tests needs to simulate lock state and,
where relevant, a cache value appearing after N polls — e.g. a counter that
flips `Get`'s return value after a configured number of calls.

`cache.go` gets two additional real-Redis integration tests (skipped when
Redis isn't reachable, matching the existing pattern in this package):

- `AcquireLock` mutual exclusion: two calls for the same ID, only one
  returns `true`.
- `ReleaseLock` clears the key: acquire, release, acquire again succeeds.

## Eviction config

`infra/docker-compose.yml`'s `redis` service gets a `command` override:

```yaml
command: redis-server --maxmemory 256mb --maxmemory-policy allkeys-lru
```

`allkeys-lru` evicts the least-recently-used key (regardless of TTL) once
`maxmemory` is reached — appropriate here since every key in this Redis
instance (content cache, negative cache, id counter, stampede locks)
benefits from LRU-style eviction, and there's no key the project needs to
protect from eviction at the cost of write errors. 256MB is a dev-scale
default; production sizing is a Phase 6 (Scaling & Ops) concern. This is a
pure infra change — no application code involved.

## Out of scope

- General cross-key thundering-herd protection / rate limiting.
- Production Redis memory sizing (Phase 6).
- Cache warming or pre-fetching strategies.
