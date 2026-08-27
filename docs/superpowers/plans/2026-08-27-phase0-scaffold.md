# Phase 0 Scaffold Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the repo skeleton and a local Docker Compose stack (Postgres, Redis, MinIO) that Phase 1+ builds on. No application code yet.

**Architecture:** CQRS-lite: `write-service/` and `read-service/` will be separate deployable Go binaries (built in later phases), sharing `shared/` internal packages. `infra/` holds the local dev stack definition and DB migrations. This task only creates the directory skeleton and the Compose stack — no Go code beyond the existing `go.mod`.

**Tech Stack:** Go 1.26 (stdlib `net/http` only, no framework), Postgres 16, Redis 7, MinIO, Docker Compose.

## Global Constraints

- Read and Write are separate services — no shared application process, separate APIs/deployments/scaling groups. (Not yet applicable in Phase 0 — no service code exists yet — but the skeleton must keep them as separate directories/binaries from the start.)
- Write path order is always: upload content to S3 first, commit metadata second. (Not applicable in Phase 0.)
- Read path order is always: cache → metadata DB → S3, cache-aside on miss, negative caching for 404s/expired. (Not applicable in Phase 0.)
- Every service must be independently runnable, testable, and have a health check endpoint before it's "done." (Health checks land in Phase 2/3 with the actual services; Phase 0 only needs the *infra* containers' own health checks.)
- Use environment variables / config files for all connection strings — never hardcode credentials or endpoints.
- Prefer boring, explicit code over clever abstractions.
- Module path: `github.com/ah-naf/pastebin` (already set in `go.mod`).
- Repo structure locked by the Phase 0 design doc (`docs/superpowers/specs/2026-08-27-pastebin-phase0-design.md`): `write-service/`, `read-service/`, `sweeper/`, `lb/`, `shared/{config,id,pgconn,cache}`, `infra/{migrations}`.
- Postgres driver decision (for later phases): `jackc/pgx` v5, stdlib-compat mode.
- Redis client decision (for later phases): `redis/go-redis`.

---

### Task 1: Repo skeleton directories

**Files:**
- Create: `write-service/.gitkeep`
- Create: `read-service/.gitkeep`
- Create: `sweeper/.gitkeep`
- Create: `lb/.gitkeep`
- Create: `shared/config/.gitkeep`
- Create: `shared/id/.gitkeep`
- Create: `shared/pgconn/.gitkeep`
- Create: `shared/cache/.gitkeep`
- Create: `infra/migrations/.gitkeep`
- Create: `.gitignore`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: directory skeleton that Task 2 (`infra/docker-compose.yml`) and all Phase 1+ plans place files into. No Go code, no exported symbols yet.

- [ ] **Step 1: Create the directory skeleton**

Git doesn't track empty directories, so each gets a `.gitkeep` placeholder:

```bash
mkdir -p write-service read-service sweeper lb \
  shared/config shared/id shared/pgconn shared/cache \
  infra/migrations
touch write-service/.gitkeep read-service/.gitkeep sweeper/.gitkeep lb/.gitkeep \
  shared/config/.gitkeep shared/id/.gitkeep shared/pgconn/.gitkeep shared/cache/.gitkeep \
  infra/migrations/.gitkeep
```

- [ ] **Step 2: Add `.gitignore`**

```
# .gitignore
/bin/
*.env
.env
```

- [ ] **Step 3: Verify structure**

Run: `find . -type d -not -path './.git*'`
Expected: lists `write-service`, `read-service`, `sweeper`, `lb`, `shared`, `shared/config`, `shared/id`, `shared/pgconn`, `shared/cache`, `infra`, `infra/migrations` (plus `.` and `docs/...`).

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "chore: scaffold repo directory structure"
```

---

### Task 2: `infra/docker-compose.yml` — Postgres, Redis, MinIO

**Files:**
- Create: `infra/docker-compose.yml`
- Create: `infra/.env.example`

**Interfaces:**
- Consumes: nothing.
- Produces: three running containers reachable at:
  - Postgres: `localhost:5432`, db `pastebin`, user/pass from `infra/.env` (copied from `.env.example`).
  - Redis: `localhost:6379`, no auth (local dev only).
  - MinIO: API `localhost:9000`, console `localhost:9001`, root user/pass from `infra/.env`.
  These addresses are what `shared/config` (Phase 1+) will read from environment variables — no service code hardcodes them.

- [ ] **Step 1: Write `infra/.env.example`**

```bash
# infra/.env.example — copy to infra/.env for local dev, never commit .env
POSTGRES_USER=pastebin
POSTGRES_PASSWORD=pastebin_dev_password
POSTGRES_DB=pastebin

MINIO_ROOT_USER=pastebin_minio
MINIO_ROOT_PASSWORD=pastebin_minio_password
```

- [ ] **Step 2: Write `infra/docker-compose.yml`**

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB}
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER} -d ${POSTGRES_DB}"]
      interval: 5s
      timeout: 3s
      retries: 5

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    volumes:
      - redisdata:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: ${MINIO_ROOT_USER}
      MINIO_ROOT_PASSWORD: ${MINIO_ROOT_PASSWORD}
    ports:
      - "9000:9000"
      - "9001:9001"
    volumes:
      - miniodata:/data
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
      interval: 5s
      timeout: 3s
      retries: 5

volumes:
  pgdata:
  redisdata:
  miniodata:
```

- [ ] **Step 3: Copy env file and start the stack**

```bash
cd infra
cp .env.example .env
docker compose up -d
```

- [ ] **Step 4: Verify all three containers report healthy**

Run: `cd infra && docker compose ps`
Expected: `postgres`, `redis`, `minio` all show `(healthy)` in the STATUS column (may take a few seconds — re-run if still `starting`).

- [ ] **Step 5: Verify each service is actually reachable**

```bash
docker compose exec postgres pg_isready -U pastebin -d pastebin
docker compose exec redis redis-cli ping
curl -f http://localhost:9000/minio/health/live
```

Expected: `accepting connections`, `PONG`, and an empty `200 OK` response respectively.

- [ ] **Step 6: Tear down (confirms clean stop/start cycle works)**

```bash
docker compose down
docker compose up -d
docker compose ps
```

Expected: same healthy state as Step 4 — proves the stack isn't relying on first-boot-only state.

- [ ] **Step 7: Commit**

```bash
cd ..
git add infra/docker-compose.yml infra/.env.example
git commit -m "chore: add docker-compose stack (postgres, redis, minio)"
```

---

## Phase 0 done-criteria checklist

- [ ] `docker compose up` (from `infra/`) starts Postgres, Redis, MinIO and all three report healthy.
- [ ] Repo structure matches the design doc (`write-service/`, `read-service/`, `sweeper/`, `lb/`, `shared/{config,id,pgconn,cache}`, `infra/migrations`).
- [ ] `go build ./...` succeeds at the repo root (trivially true — no `.go` files yet, nothing to fail).
- [ ] `infra/.env` is gitignored; only `.env.example` is committed.

Once checked, Phase 0 is done. Next: Phase 1 (ID generation — `shared/id`), which is where the test-first cycle (write failing test → implement → verify) actually starts, since Phase 0 has no application logic to test.
