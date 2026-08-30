# Phase 6 (part 1): Load Balancer — Design

## Goal

Provide a load balancer in front of the write-service and read-service, as
originally scoped back in Phase 0 but deferred. Routes requests to the
correct service by method+path, round-robins across multiple replicas of
each, and stays useful even when a replica dies (health checks + one retry).

## Scope

This is the first of four independent sub-projects under Phase 6 (Scaling &
Ops); the others — metrics, read-replica scaling config, and the ops
runbook — get their own specs later. This spec covers only the load
balancer itself.

Out of scope for this spec:
- Containerizing write-service/read-service (they still run via
  `go run ./cmd/...` on the host, same as today; the LB is just another
  service run the same way, pointed at whatever host:port addresses the
  operator gives it).
- TLS termination.
- Any change to write-service or read-service application code.

## Architecture

New top-level `lb/` directory, structured like the existing
`write-service/`/`read-service/`/`sweeper` directories — its own
`cmd/lb/main.go` plus `internal/` packages, run the same way
(`go run ./cmd/lb`) rather than as a container-only artifact, since
write-service and read-service aren't containerized yet either.

Two packages:

- **`lb/internal/pool`** — `Pool` holds a list of backend addresses with
  per-backend health state.
  - `Next() (*Backend, bool)` round-robins (atomic counter) among
    currently-healthy backends, skipping unhealthy ones; returns
    `(nil, false)` if none are healthy.
  - `StartHealthChecks(ctx context.Context, interval time.Duration, client *http.Client)`
    runs a background loop: every `interval`, `GET {backend}/healthz` for
    every backend in the pool; a 200 response marks it healthy, anything
    else (including a timeout) marks it unhealthy. Stops when `ctx` is
    canceled.

- **`lb/internal/proxy`** — `Proxy` wraps `httputil.ReverseProxy` per
  request.
  - `New(pool *pool.Pool) *Proxy`, implements `http.Handler`.
  - On each request: `pool.Next()` picks a backend, request is proxied to
    it via `httputil.ReverseProxy`. If the backend fails before any
    response bytes are written — a dial/connect failure, the only case
    `ReverseProxy`'s `ErrorHandler` fires for — retry once against a
    different backend from `pool.Next()`. If that also fails (or no other
    backend is available), respond 502. If the pool has no healthy backend
    at all, respond 503 immediately without attempting a proxy call.

`lb/cmd/lb/main.go` wires two independent `Pool`+`Proxy` pairs — one for
write backends, one for read backends — routed by the same method+path
patterns the backend services already register themselves:

```go
mux.Handle("POST /paste", writeProxy)
mux.Handle("GET /paste/{id}", readProxy)
mux.HandleFunc("GET /healthz", lbHealthz(writePool, readPool))
```

The LB's own `/healthz` reports 200 only if both pools have at least one
healthy backend, 503 otherwise (mirrors the aggregate-dependency healthz
pattern already used by write-service/read-service for their own
dependencies).

Graceful shutdown uses the same `signal.NotifyContext` + `server.Shutdown`
pattern as write-service/read-service/sweeper's HTTP-serving code; the
health-check goroutines stop via the same shutdown context being canceled.

## Configuration

Small enough it doesn't need `shared/config` — that struct requires
`DATABASE_URL`/S3 credentials the LB has no use for, and forcing those as
"required" for a service that never touches them would be wrong. Instead,
`lb/cmd/lb/main.go` reads its own env vars directly via `os.Getenv`:

- `WRITE_BACKENDS` — comma-separated `http://host:port` list (required).
- `READ_BACKENDS` — comma-separated `http://host:port` list (required).
- `PORT` — LB's own listen port (default `8082`).
- `HEALTH_CHECK_INTERVAL` — e.g. `5s`, parsed with `time.ParseDuration`
  (default `5s`).

## Data flow

Request in → mux dispatches by method+path → matched `Proxy.ServeHTTP`:

1. `pool.Next()` — round-robin cursor advances, skipping any backend
   currently marked unhealthy. All unhealthy → 503
   `"no healthy backend available"`.
2. Request is rewritten to the chosen backend's address and forwarded via
   `httputil.ReverseProxy`.
3. Backend responds normally → response streamed straight through,
   unchanged (status, headers, body).
4. Backend unreachable (dial/connect failure) → retry once against a
   different backend from `pool.Next()`. Second failure, or no second
   backend available → 502.

Health-check loop runs independently of request handling: every
`HEALTH_CHECK_INTERVAL`, `GET {backend}/healthz` for every backend in both
pools; result updates that backend's health flag for the next `Next()`
call.

## Interfaces & testing

- **`pool.Pool`**: `New(addrs []string) *Pool`, `Next() (*Backend, bool)`,
  `StartHealthChecks(ctx, interval, client)`.
  - Unit tests drive health state directly (an unexported setter,
    package-internal test) to check round-robin skips unhealthy backends
    and returns `false` when all are down — no real HTTP needed.
  - A separate integration-style test spins up `httptest.Server` fakes
    (one healthy, one returning 500) and confirms `StartHealthChecks`
    converges the pool's health state after one interval.

- **`proxy.Proxy`**: `New(pool *pool.Pool) *Proxy`, implements
  `http.Handler`. Tests use `httptest.Server` backends — one normal, one
  closed/unreachable:
  - Normal case proxies through untouched (status, headers, body).
  - One backend down, a second healthy → the down one is retried against,
    request still succeeds via the healthy one.
  - All backends down after retry → 502.
  - Empty/no-healthy-backend pool → 503, no proxy attempt made.

## Out of scope

- Metrics, read-replica scaling config, and the ops runbook (separate
  Phase 6 sub-projects).
- Containerizing write-service/read-service.
- TLS termination.
- Weighted/least-connections load balancing (plain round-robin only).
