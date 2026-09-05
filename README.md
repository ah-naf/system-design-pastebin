# Pastebin

A production-style pastebin clone in Go, built as separate **write** and
**read** services (CQRS-lite) rather than one monolith: the write path never
serves reads, the read path never accepts writes. Paste content lives in S3
(MinIO locally), metadata in Postgres, and Redis is a cache-aside layer that
the system is designed to keep working without (Redis being down degrades
performance, never correctness).

## Architecture

```
                     ┌──────────────┐
        POST /paste  │              │
       ─────────────▶│      lb      │──▶ write-service (×N) ──▶ Postgres + S3
                      │ (round-robin,│
        GET /paste/id │  health-     │
       ─────────────▶│  checked,    │──▶ read-service (×N)  ──▶ Redis cache
                      │  retry-once) │                          → Postgres + S3
                      └──────────────┘

        sweeper (one-shot CLI, run on a schedule) ──▶ deletes expired pastes
                                                        from Postgres + S3
```

| Component | What it does | Port (default) |
|---|---|---|
| `write-service` | `POST /paste` — validates, uploads to S3, writes metadata to Postgres | 8080 |
| `read-service` | `GET /paste/{id}` — cache-aside read (Redis → Postgres → S3) | 8081 |
| `lb` | Load balancer in front of write/read replicas — round-robin, health-checked, retries once on a dead backend | 8082 |
| `sweeper` | One-shot batch job: deletes expired pastes from Postgres + S3 | n/a (run manually or on a cron) |

Each service is its own binary under `cmd/<service>/main.go`, all in one Go
module (`github.com/ah-naf/pastebin`).

## Prerequisites

- Go 1.26+
- Docker + Docker Compose (for Postgres, Redis, MinIO)

## 1. Start the infra stack

```bash
cd infra
cp .env.example .env
```

Edit `infra/.env` if you want non-default credentials, or if port 5432 is
already taken on your machine (a native Postgres install, say) — set
`POSTGRES_HOST_PORT` to something else, e.g. `5433`. The container always
listens on 5432 internally regardless.

```bash
docker compose up -d
```

This starts Postgres, Redis, and MinIO, each bound to `127.0.0.1` only.
Check they're healthy:

```bash
docker compose ps
```

## 2. Configure the services

All services read config from environment variables (see
`shared/config/config.go` for the full list and defaults). At minimum you need:

| Variable | Required? | Example | Notes |
|---|---|---|---|
| `DATABASE_URL` | yes | `postgres://pastebin:pastebin_dev_password@localhost:5432/pastebin?sslmode=disable` | match whatever you put in `infra/.env` |
| `S3_ACCESS_KEY` | yes | `pastebin_minio` | matches `MINIO_ROOT_USER` in `infra/.env` |
| `S3_SECRET_KEY` | yes | `pastebin_minio_password` | matches `MINIO_ROOT_PASSWORD` in `infra/.env` |
| `ID_XOR_SECRET` | write-service only | `0123456789abcdef` | 16 hex chars; obfuscates paste IDs. Any value works locally — just pick one and keep it consistent across write-service restarts (existing IDs decode against whatever secret generated them, though decoding isn't required for normal operation) |

Everything else (`PORT`, `REDIS_ADDR`, `S3_ENDPOINT`, `S3_BUCKET`,
`MAX_PASTE_BYTES`, DB connection pool sizes, etc.) has a sane default for
local dev — override only if you need to.

**Important:** run every `go run` command from the **repo root**, not from
inside a service's own directory. `write-service` runs its Postgres
migrations from the relative path `infra/migrations`, which only resolves
correctly when your working directory is the repo root.

## 3. Run the services

Each in its own terminal, from the repo root:

```bash
# write-service
DATABASE_URL="postgres://pastebin:pastebin_dev_password@localhost:5432/pastebin?sslmode=disable" \
S3_ACCESS_KEY="pastebin_minio" S3_SECRET_KEY="pastebin_minio_password" \
ID_XOR_SECRET="0123456789abcdef" \
go run ./write-service/cmd/write-service

# read-service
DATABASE_URL="postgres://pastebin:pastebin_dev_password@localhost:5432/pastebin?sslmode=disable" \
S3_ACCESS_KEY="pastebin_minio" S3_SECRET_KEY="pastebin_minio_password" \
go run ./read-service/cmd/read-service
```

At this point you already have a working pastebin — `write-service` on
`:8080`, `read-service` on `:8081`. The load balancer is optional for local
single-replica use, but here's how to add it (and how to run multiple
replicas of each service, which is the whole point of an LB):

```bash
# a second write-service replica, same DB/S3, different port
DATABASE_URL="..." S3_ACCESS_KEY="..." S3_SECRET_KEY="..." ID_XOR_SECRET="..." \
PORT=8090 PUBLIC_BASE_URL="http://localhost:8082" \
go run ./write-service/cmd/write-service

# lb, pointed at both write-service ports
WRITE_BACKENDS="http://localhost:8080,http://localhost:8090" \
READ_BACKENDS="http://localhost:8081" \
go run ./lb/cmd/lb
```

`lb` listens on `:8082` by default and routes `POST /paste` /
`GET /paste/{id}` to the matching backend pool, health-checking each backend
via its `/healthz` every 5s and retrying once if a request happens to land
on one that just died.

## 4. Try it

```bash
# create a paste (through the LB, port 8082 — or hit write-service on 8080 directly)
curl -X POST http://localhost:8082/paste -d '{"content":"hello, pastebin"}'
# → {"id":"abc123","url":"http://localhost:8082/paste/abc123"}

# read it back
curl http://localhost:8082/paste/abc123

# health checks
curl http://localhost:8080/healthz   # write-service
curl http://localhost:8081/healthz   # read-service
curl http://localhost:8082/healthz   # lb (aggregate: both pools must have a healthy backend)
```

Optional fields on create: `"expires_in_seconds": 3600` to make a paste
expire (the sweeper then cleans it up).

## 5. Run the sweeper (expired-paste cleanup)

One-shot CLI, no HTTP server — run it manually or wire it into a cron:

```bash
DATABASE_URL="..." S3_ACCESS_KEY="..." S3_SECRET_KEY="..." \
go run ./sweeper/cmd/sweeper
```

Logs how many expired pastes it deleted, then exits.

## Running tests

```bash
go build ./...      # everything compiles
go vet ./...         # static checks
go test ./...        # unit tests; a few integration tests auto-skip if
                      # Postgres/Redis/MinIO aren't reachable at localhost
```

## Project structure

```
write-service/   POST /paste — validation, S3 upload, Postgres insert
read-service/    GET /paste/{id} — cache-aside (Redis → Postgres → S3)
sweeper/         one-shot expired-paste cleanup
lb/              load balancer: round-robin pool + health checks + retry-once proxy
shared/          cross-service packages: config loading, Postgres connection/migrations,
                 Redis client, base62 ID generation
infra/           docker-compose.yml (Postgres/Redis/MinIO) + Postgres migrations
docs/superpowers/
  specs/         design docs, one per feature
  plans/         implementation plans, one per feature
```

Each service directory follows the same shape: `cmd/<service>/main.go` for
the entrypoint, `internal/` for its own packages (never imported by other
services — only `shared/` is shared).
