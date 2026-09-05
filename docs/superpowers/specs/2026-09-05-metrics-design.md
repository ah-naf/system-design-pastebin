# Phase 6 (part 2): Metrics — Design

## Goal

Instrument write-service, read-service, and the load balancer with OpenTelemetry
metrics, expose them via Prometheus, and visualize them in Grafana — enough to
watch this system behave like a real production service, not just pass tests.

## Concepts glossary

(Written for someone with zero prior metrics/observability background —
refer back to this while implementing.)

- **Metric** — a number about your system, tracked over time.
- **Counter** — only ever goes up (e.g. total requests served). Resets only
  when the process restarts.
- **UpDownCounter / Gauge** — can go up or down (e.g. backends currently
  healthy).
- **Histogram** — records a *distribution* of values, not a single number
  (e.g. request latency). Lets you ask "what's the 95th-percentile latency?"
  instead of just the average, which hides outliers.
- **OpenTelemetry (OTel)** — a vendor-neutral standard for producing
  telemetry (metrics, traces, logs). You instrument against OTel's API once;
  the backend (Prometheus here, could be something else later) is swappable
  without touching instrumented code. Has an **API** (what your code calls,
  e.g. `counter.Add(ctx, 1)`) and an **SDK** (does the aggregation and
  exporting).
- **MeterProvider** — the OTel SDK object that owns metric collection
  configuration for a process; you create one at startup.
- **Meter** — obtained from a `MeterProvider`, the thing you create
  instruments (counters/histograms/gauges) from.
- **Exporter** — turns collected metric data into a specific backend's
  format. Here: the OTel Prometheus exporter, which exposes metrics in the
  text format Prometheus expects.
- **Prometheus** — a time-series database that *pulls* (scrapes) metrics
  from a `/metrics` HTTP endpoint on a schedule (e.g. every 5s), rather than
  services pushing data to it.
- **Grafana** — a dashboard/visualization tool that queries a data source
  (Prometheus here) and renders graphs.
- **RED method** — a common baseline for what to measure on any service:
  **R**ate (requests/sec), **E**rrors (error rate), **D**uration (latency).

## Scope

Third of four Phase 6 (Scaling & Ops) sub-projects (after the load
balancer; before read-replica scaling config and the ops runbook).

In scope:
- RED metrics (request count, request duration) for write-service,
  read-service, and the load balancer.
- Domain-specific metrics: cache hit/miss/negative counts (read-service),
  paste size distribution (write-service), healthy-backend-count and
  retry-count (load balancer).
- A `/metrics` endpoint on each of the three services, scraped by a new
  Prometheus container.
- A new Grafana container, pre-provisioned with Prometheus as its data
  source and one starter dashboard.

Out of scope:
- The sweeper (one-shot CLI; Prometheus's pull model doesn't fit a process
  that's already exited by scrape time — would need a Pushgateway, not
  worth it for one batch job at this scale).
- Traces (a separate OTel signal from metrics).
- Logging changes — stays plain stdlib `log` as it is today. OTel has a
  Logs signal too, but adopting it (structured logging, a log exporter,
  something to view logs like Grafana Loki) is a bigger, separate change;
  keeping this phase metrics-only matches the original Phase 6 scope.
- Alerting rules (Prometheus/Grafana both support this; not built here).
- Production-scale Prometheus/Grafana deployment concerns (retention,
  HA, auth) — this is a dev-stack addition, same posture as the existing
  postgres/redis/minio containers.

## Architecture

### `shared/metrics` package

New package, same precedent as `shared/config`/`shared/cache`/`shared/pgconn`.

```go
// Init sets up an OTel MeterProvider backed by the Prometheus exporter for
// serviceName, returning the /metrics HTTP handler to mount and a shutdown
// func to call during graceful shutdown (flushes final data).
func Init(serviceName string) (handler http.Handler, shutdown func(context.Context) error, err error)

// HTTPMiddleware wraps mux, recording RED metrics (request count, duration
// histogram) for every request before delegating to mux's own routing.
// Route label comes from mux.Handler(r) (stdlib ServeMux method that looks
// up which registered pattern would handle r, without invoking it) so
// metrics are labeled by pattern (e.g. "GET /paste/{id}"), not raw path.
func HTTPMiddleware(mux *http.ServeMux, meter metric.Meter) (http.Handler, error)
```

Each service's `main.go` calls `metrics.Init(serviceName)` once at startup,
mounts the returned handler at `GET /metrics` on its existing mux (alongside
`/healthz` and its real routes), wraps the whole mux with `HTTPMiddleware`,
and calls `shutdown` during the existing graceful-shutdown sequence.

`HTTPMiddleware` is unit-testable without any real Prometheus/network
dependency: OTel's SDK ships a `manualreader` (`go.opentelemetry.io/otel/sdk/metric/metricdata`
+ `metric.NewManualReader()`) built exactly for this — construct a test
`MeterProvider` with one, fire requests through `httptest.NewRecorder`, call
`reader.Collect(ctx, &data)`, and assert the resulting `metricdata.ResourceMetrics`
has the expected counts/attributes.

### Domain-specific metrics

One small typed wrapper per service, following the same interface-seam
pattern already used throughout this codebase — an interface added to the
`Handler`/`Pool` struct's constructor, a fake in existing handler/pool
tests, and a real OTel-backed implementation that gets its own test via the
same `manualreader` technique:

- **read-service** (`read-service/internal/metrics/cache_metrics.go`):
  ```go
  type CacheMetricsRecorder interface {
      RecordResult(ctx context.Context, result cache.Result)
  }
  ```
  Real implementation: one `Int64Counter` labeled by an attribute
  `result="hit"|"miss"|"negative"`. Wired into `handler.Handler` alongside
  the existing `CacheGetter`/`CacheSetter`/`Repository`/`StoreRepository`;
  called right after the existing `h.cacheGetter.Get(ctx, id)` switch, for
  every branch (`Hit`, `Negative`, and the eventual `Miss` outcome).

- **write-service** (`write-service/internal/metrics/paste_metrics.go`):
  ```go
  type PasteMetricsRecorder interface {
      RecordPasteSize(ctx context.Context, size int64)
  }
  ```
  Real implementation: one `Int64Histogram`. Called in `CreatePaste` right
  after a successful store+repo write (mirrors where `SetPositive`-style
  post-success hooks already sit in the read-service handler).

- **lb** (`lb/internal/metrics/pool_metrics.go`):
  ```go
  type PoolMetricsRecorder interface {
      SetHealthyCount(pool string, count int)
      RecordRetry(pool string)
  }
  ```
  Real implementation: one `Int64Gauge`-equivalent (OTel's async/observable
  gauge, or a synchronous `Int64UpDownCounter` reset-and-set each health
  check pass — implementation detail decided in the plan) labeled by
  `pool="write"|"read"`, and one `Int64Counter` for retries. `SetHealthyCount`
  called from the health-check loop (Task 2 of the LB plan) after each pass;
  `RecordRetry` called from `proxy.Proxy.forward` right where it currently
  decides to retry.

### Infra additions (`infra/docker-compose.yml` + new files)

- **`prometheus`** service (`prom/prometheus:latest`), config file
  `infra/prometheus.yml` with scrape targets `host.docker.internal:8080/metrics`,
  `host.docker.internal:8081/metrics`, `host.docker.internal:8082/metrics`
  (write-service, read-service, lb — all run on the host via `go run`, not
  containerized; `host.docker.internal` is Docker Desktop's built-in DNS
  name for reaching the host from inside a container, no extra config needed
  on this Windows/Docker-Desktop machine). Exposed at `127.0.0.1:9090`.
- **`grafana`** service (`grafana/grafana:latest`), exposed at
  `127.0.0.1:3000`, with two provisioning files under `infra/grafana/`:
  a datasource pointing at the `prometheus` service, and one starter
  dashboard (request rate, error rate, p50/p95/p99 latency, cache hit
  ratio, healthy-backend count) — provisioned automatically on container
  start, no manual click-through setup.

### New dependencies

First non-stdlib additions since `pgx`/`aws-sdk-go-v2`/`go-redis`:
`go.opentelemetry.io/otel`, `go.opentelemetry.io/otel/sdk`,
`go.opentelemetry.io/otel/exporters/prometheus`, and
`github.com/prometheus/client_golang` (pulled in transitively to serve the
`/metrics` HTTP handler in the format Prometheus expects).

## Testing

- `shared/metrics.HTTPMiddleware`: unit tests using OTel's `manualreader`
  (no live Prometheus/network needed) — verify request count increments
  correctly per method+pattern+status, duration histogram records a
  positive value, route label reflects the matched pattern rather than the
  raw path (e.g. a request to `/paste/abc123` labels as `GET /paste/{id}`).
- Each domain-specific real recorder (`CacheMetricsRecorder`,
  `PasteMetricsRecorder`, `PoolMetricsRecorder` implementations): same
  `manualreader` technique, own test file per package.
- Existing handler/pool tests in all three services: add a
  `fake...Recorder` alongside their existing fakes, assert the handler/pool
  calls it with the expected arguments on each relevant path — no changes
  to existing test assertions, purely additive.
- `metrics.Init` and the Prometheus/Grafana docker-compose additions: no
  automated test (infra wiring, like the Task 3 Redis eviction config in
  Phase 5) — manual verification via `curl localhost:PORT/metrics` and
  opening Grafana in a browser to confirm the dashboard renders live data.

## Out of scope (restated)

- Sweeper metrics (would need a Pushgateway).
- Traces.
- Logging changes (stays plain stdlib `log`).
- Alerting.
- Production Prometheus/Grafana hardening (retention, HA, auth).
