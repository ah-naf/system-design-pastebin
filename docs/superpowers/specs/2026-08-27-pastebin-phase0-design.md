# Pastebin CQRS Build — Phase 0 Design

## Context

Building a production-grade Pastebin clone as a CQRS-lite system: separate Write
Service and Read Service, each independently deployable/scalable, sharing a
Postgres metadata store initially. Content lives in S3 (MinIO locally); Redis
is cache-only (never a hard dependency). Full 8-phase build order and hard
constraints are defined by the governing project brief (see repo root context);
this document covers only the Phase 0 scaffolding decisions and locks them in
before any code is written.

Traffic target: ~12 QPS avg / 35 QPS peak writes, ~116 QPS avg / 350 QPS peak
reads (10:1 read:write). p99 read latency < 100ms.

## Goals

- Confirm every open tech decision needed to start Phase 0.
- Produce a repo structure and docker-compose stack that Phase 1+ builds on.
- Keep dependency count minimal; use Go stdlib (`net/http`, `database/sql`)
  wherever it's sufficient.

## Decisions

### Language & HTTP
Go 1.26, stdlib `net/http` for all services. No web framework.

### Metadata store
Postgres. Driver: **`jackc/pgx` (pgx/v5, stdlib-compat mode via `database/sql`)**
— actively maintained, fast, works through the standard `sql.DB` interface so
service code stays idiomatic stdlib `database/sql` calls.

### Cache
Redis. Client: **`redis/go-redis`** — hand-rolling RESP (pooling, pipelining,
reconnect/timeout handling) was considered for a true zero-dependency setup,
but the correctness risk (connection pool bugs, silent reconnect failures)
outweighs the one extra dependency. Cache is explicitly non-critical-path per
Hard Constraint #3/#5, so this dependency is isolated to a thin `shared/cache`
wrapper — losing it degrades, not breaks, the Read Service.

### Object storage
S3-compatible. MinIO locally via docker-compose. Client library TBD in
Phase 2/3 (AWS SDK v2 minimal S3 client, decided when Write/Read Service
design starts).

### ID generation (Phase 1 detail, locked now)
Redis-backed atomic counter, not Snowflake:
1. `INCR` a Redis key → monotonic `uint64`.
2. XOR the counter with a secret (`ID_XOR_SECRET` env var, fixed-width key)
   to break sequential guessability. XOR is self-inverse, so the same
   operation reverses it for internal debugging — this reversal is never
   exposed via any API.
3. Base62-encode the XORed value → the public paste ID.

Rationale: at 12-35 QPS peak writes, a single Redis counter has no contention
problem. Snowflake-style (timestamp+node+sequence) avoids the Redis
round-trip but adds node-ID assignment and clock-drift handling that aren't
justified at this scale. Redis is already in the stack for caching, so this
adds no new infrastructure.

### Load balancer
**Built in Go**, not an external nginx/Traefik container — matches the
native-http, low-dependency, learn-every-moving-part goals. Implementation:
`net/http/httputil.ReverseProxy`, round-robin over a backend list read from
an env var (e.g. `WRITE_BACKENDS=host1:port,host2:port`), skipping any
backend currently failing its `/healthz` check. **Two separate LB
processes/binaries** — one fronting Write Service replicas, one fronting
Read Service replicas — mirroring the CQRS separation (Hard Constraint #1)
rather than one LB process routing both.

### Module path
`github.com/ah-naf/pastebin` (already created; `go.mod` present at repo root).

## Repo structure

```
pastebin/
  go.mod
  write-service/     cmd/ + internal/, own main.go, own health check
  read-service/       cmd/ + internal/, own main.go, own health check
  sweeper/            Phase 4 stub (separate process, not built yet)
  lb/                  reverse-proxy LB; two entrypoints (write-lb, read-lb) or
                       one binary parameterized by role via env/flag
  shared/
    config/            env var loading, shared across all services
    id/                 Phase 1: counter + XOR + base62 (used only by write-service)
    pgconn/            Postgres connection helper (pgx/v5 stdlib mode)
    cache/              Redis wrapper (go-redis), fail-open on error
  infra/
    docker-compose.yml  Postgres, Redis, MinIO — nothing else yet
    migrations/          SQL migration files (Phase 2 adds the first one)
```

## Testing approach

Test-first per unit: for each module/endpoint, tests are written before the
corresponding implementation. Go's stdlib `testing` package only — no
testify/ginkgo — table-driven tests, matching the low-dependency constraint.
User implements after tests are written; tests are the acceptance check.

## Out of scope for this document

Everything from Phase 1 onward (ID module internals' test cases, Write
Service endpoint, Read Service endpoint, sweeper, cache stampede protection,
metrics/observability, CDN) is already sequenced by the governing project
brief's Build Order and is designed phase-by-phase as each is reached, not
here. This document only unblocks Phase 0.

## Testing/validation for Phase 0 itself

Phase 0 has no application logic to unit test. Its "done" bar (per the
governing brief):
- `docker-compose up` starts Postgres, Redis, MinIO and all three report
  healthy.
- Repo structure above exists with empty/stub `main.go` files where
  applicable.
- `go build ./...` succeeds at the repo root.
