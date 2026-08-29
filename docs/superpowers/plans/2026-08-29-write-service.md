# Write Service Implementation Plan

> **Execution mode:** Task 1 is infra housekeeping (fixing a parked issue from the Phase 0 review) — Claude does this directly, no test-first pairing, since it's config not application logic. Tasks 2-7 continue the Phase 1 pattern: Claude writes each task's failing test, the user writes the implementation (signatures below, not filled-in code — the user is the implementer here by their own stated preference, for a learning-by-building project), Claude reviews the diff before moving on. Task 7 (main.go wiring) has no Go test file — its "test" is a manual curl walkthrough, run together.

**Goal:** Build the Write Service: `POST /paste` (generate ID, upload to S3, write metadata) and `GET /healthz` (readiness).

**Architecture:** Three testable-in-isolation layers behind interfaces — `db.Repo` (Postgres), `storage.Store` (S3/MinIO), `handler.Handler` (HTTP, depends only on interfaces so its tests use fakes) — wired together in `cmd/write-service/main.go`. Same interface-seam pattern `shared/id` established in Phase 1.

**Tech Stack:** Go 1.26 stdlib `net/http`/`database/sql`, `jackc/pgx/v5` (Postgres driver), `aws/aws-sdk-go-v2` (S3 client), `golang-migrate/migrate/v4` (schema migrations), `redis/go-redis/v9` (already a dependency from Phase 1).

## Global Constraints

- Module path: `github.com/ah-naf/pastebin`.
- Write order is always S3 upload first, metadata commit second — never the reverse (Hard Constraint #2).
- Every dependency the handler touches is an interface; fakes drive handler tests, no real Postgres/S3/Redis needed for that suite.
- `expires_in_seconds` omitted or `0` → `expires_at` is `NULL` (never expires).
- Orphaned S3 objects (upload succeeded, DB insert failed) are accepted, not cleaned up — see design doc's Error Handling section. Do not add compensating-delete logic.
- Env vars, never hardcoded credentials/endpoints (project-wide Hard Constraint #5).
- Tests: stdlib `testing` only, table-driven where it fits.
- Integration tests (Postgres/MinIO) skip cleanly (via `t.Skip`) when the dependency is unreachable, matching Phase 1's `requireRedis` pattern — `go test ./...` must never hard-fail on a machine without Docker running.

---

### Task 1: Infra fix — Postgres port conflict + parked Phase 0 minors

Fixes the finding parked in Phase 0's final review: a native Windows PostgreSQL service holds host port 5432 on this machine, so the compose Postgres container's port mapping doesn't actually bind — any host-side connection to `localhost:5432` silently hits the wrong database. This must be fixed before Task 3, which makes the first real host-side Postgres connection. Also folds in the other 4 parked minors (fail-fast env interpolation, MinIO healthcheck robustness, loopback-only binds, redundant `.gitignore` line) per the reviewer's own recommendation to bundle them into the next commit that touches `docker-compose.yml`.

**Files:**
- Modify: `infra/docker-compose.yml`
- Modify: `infra/.env.example`
- Modify: `.gitignore`

**Interfaces:**
- Produces: the Postgres host port becomes configurable via `POSTGRES_HOST_PORT` (defaults to `5432` when unset, via Compose's `${VAR:-default}` syntax). `shared/config` (Task 2) must read the actual port from `DATABASE_URL` — never hardcode `localhost:5432`.

- [ ] **Step 1: Claude rewrites `infra/docker-compose.yml`**

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: ${POSTGRES_USER:?copy infra/.env.example to infra/.env and set POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?copy infra/.env.example to infra/.env and set POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB:?copy infra/.env.example to infra/.env and set POSTGRES_DB}
    ports:
      - "127.0.0.1:${POSTGRES_HOST_PORT:-5432}:5432"
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
      - "127.0.0.1:6379:6379"
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
      MINIO_ROOT_USER: ${MINIO_ROOT_USER:?copy infra/.env.example to infra/.env and set MINIO_ROOT_USER}
      MINIO_ROOT_PASSWORD: ${MINIO_ROOT_PASSWORD:?copy infra/.env.example to infra/.env and set MINIO_ROOT_PASSWORD}
    ports:
      - "127.0.0.1:9000:9000"
      - "127.0.0.1:9001:9001"
    volumes:
      - miniodata:/data
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 5s
      timeout: 3s
      retries: 5

volumes:
  pgdata:
  redisdata:
  miniodata:
```

(`minio/minio:latest` stays unpinned — pinning a specific `RELEASE.*` tag is
still worth doing but deferred, since guessing a tag string here risks
pinning one that doesn't exist. The healthcheck switch to `mc ready local`,
MinIO's own bundled readiness command, was the actual robustness fix the
review flagged — it no longer depends on `curl` being present in the image.)

- [ ] **Step 2: Claude updates `infra/.env.example`**

```bash
# infra/.env.example — copy to infra/.env for local dev, never commit .env
POSTGRES_USER=pastebin
POSTGRES_PASSWORD=pastebin_dev_password
POSTGRES_DB=pastebin
# Host-side port for Postgres. Change this if port 5432 is already taken
# on your machine (e.g. by a native PostgreSQL install) — the container
# still listens on 5432 internally either way.
POSTGRES_HOST_PORT=5432

MINIO_ROOT_USER=pastebin_minio
MINIO_ROOT_PASSWORD=pastebin_minio_password
```

- [ ] **Step 3: Claude sets this machine's actual port in `infra/.env` (untracked)**

This machine has a native PostgreSQL service on 5432, so set
`POSTGRES_HOST_PORT=5433` in `infra/.env` (not `.env.example` — that stays
the generic default). Note the value for Task 2 (`DATABASE_URL` will need
port `5433` on this machine).

- [ ] **Step 4: Claude drops the redundant `.gitignore` line**

`*.env` already matches `.env` at every depth, so remove the separate
`.env` line — `.gitignore` becomes:

```
# .gitignore
/bin/
*.env
```

- [ ] **Step 5: Claude verifies with a full restart cycle**

```bash
cd infra
docker compose down
docker compose up -d
docker compose ps
```

Expected: all three `(healthy)`. Then confirm Postgres is now reachable
from the host at the new port:

```bash
docker compose exec postgres pg_isready -U pastebin -d pastebin
```

(This still uses `docker compose exec`, same as Phase 0 — the actual
host-side reachability proof comes in Task 3, once `shared/pgconn.Open`
exists and can dial `localhost:5433` directly.)

- [ ] **Step 6: Commit**

```bash
cd ..
git add infra/docker-compose.yml infra/.env.example .gitignore
git commit -m "fix: resolve Postgres port conflict, harden compose stack"
```

---

### Task 2: `shared/config`

**Files:**
- Test: `shared/config/config_test.go`
- Implementation (user writes this): `shared/config/config.go`

**Interfaces:**
- Consumes: nothing (pure env var parsing).
- Produces:
  ```go
  type Config struct {
      Port          string // e.g. "8080"
      PublicBaseURL string // e.g. "http://localhost:8081"
      DatabaseURL   string
      RedisAddr     string
      S3Endpoint    string
      S3AccessKey   string
      S3SecretKey   string
      S3Bucket      string
      S3UseSSL      bool
      IDXORSecret   uint64
      MaxPasteBytes int64
  }
  func Load() (*Config, error)
  ```
  Task 3-7 all take a `*Config` (or its individual fields) as input — this is the one place every other task's env-var reads route through.

- [ ] **Step 1: Claude writes the failing test**

```go
package config

import "testing"

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5433/pastebin?sslmode=disable")
	t.Setenv("S3_ACCESS_KEY", "test-access-key")
	t.Setenv("S3_SECRET_KEY", "test-secret-key")
	t.Setenv("ID_XOR_SECRET", "9f3a1c2e5b7d0f14")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want \"8080\"", cfg.Port)
	}
	if cfg.PublicBaseURL != "http://localhost:8081" {
		t.Errorf("PublicBaseURL = %q, want \"http://localhost:8081\"", cfg.PublicBaseURL)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want \"localhost:6379\"", cfg.RedisAddr)
	}
	if cfg.S3Endpoint != "localhost:9000" {
		t.Errorf("S3Endpoint = %q, want \"localhost:9000\"", cfg.S3Endpoint)
	}
	if cfg.S3Bucket != "pastebin" {
		t.Errorf("S3Bucket = %q, want \"pastebin\"", cfg.S3Bucket)
	}
	if cfg.S3UseSSL != false {
		t.Errorf("S3UseSSL = %v, want false", cfg.S3UseSSL)
	}
	if cfg.MaxPasteBytes != 1048576 {
		t.Errorf("MaxPasteBytes = %d, want 1048576", cfg.MaxPasteBytes)
	}
}

func TestLoadParsesXORSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ID_XOR_SECRET", "00000000000000ff")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.IDXORSecret != 0xff {
		t.Errorf("IDXORSecret = %#x, want 0xff", cfg.IDXORSecret)
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PORT", "9090")
	t.Setenv("S3_USE_SSL", "true")
	t.Setenv("MAX_PASTE_BYTES", "2048")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want \"9090\"", cfg.Port)
	}
	if cfg.S3UseSSL != true {
		t.Errorf("S3UseSSL = %v, want true", cfg.S3UseSSL)
	}
	if cfg.MaxPasteBytes != 2048 {
		t.Errorf("MaxPasteBytes = %d, want 2048", cfg.MaxPasteBytes)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Error("Load() with empty DATABASE_URL: expected error, got nil")
	}
}

func TestLoadRequiresS3Credentials(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("S3_ACCESS_KEY", "")
	if _, err := Load(); err == nil {
		t.Error("Load() with empty S3_ACCESS_KEY: expected error, got nil")
	}
}

func TestLoadRequiresIDXORSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ID_XOR_SECRET", "")
	if _, err := Load(); err == nil {
		t.Error("Load() with empty ID_XOR_SECRET: expected error, got nil")
	}
}

func TestLoadRejectsMalformedXORSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ID_XOR_SECRET", "not-hex-at-all")
	if _, err := Load(); err == nil {
		t.Error("Load() with non-hex ID_XOR_SECRET: expected error, got nil")
	}
}

func TestLoadRejectsMalformedMaxPasteBytes(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MAX_PASTE_BYTES", "not-a-number")
	if _, err := Load(); err == nil {
		t.Error("Load() with non-numeric MAX_PASTE_BYTES: expected error, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./shared/config/... -v`
Expected: FAIL to compile — `undefined: Load` / `undefined: Config`.

- [ ] **Step 3: User writes `shared/config/config.go`**

Reads env vars with `os.Getenv`/`os.LookupEnv`. Required (error if empty):
`DATABASE_URL`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `ID_XOR_SECRET`. Optional
with defaults: `PORT` (`"8080"`), `PUBLIC_BASE_URL`
(`"http://localhost:8081"`), `REDIS_ADDR` (`"localhost:6379"`),
`S3_ENDPOINT` (`"localhost:9000"`), `S3_BUCKET` (`"pastebin"`),
`S3_USE_SSL` (`"false"`, parsed with `strconv.ParseBool`), `MAX_PASTE_BYTES`
(`"1048576"`, parsed with `strconv.ParseInt`). `ID_XOR_SECRET` is parsed
with `strconv.ParseUint(s, 16, 64)` (same as Phase 1's design). A parse
failure on any of `S3_USE_SSL`, `MAX_PASTE_BYTES`, or `ID_XOR_SECRET`
returns an error from `Load`, same as a missing required var.

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./shared/config/... -v`
Expected: PASS (all 8 test functions).

- [ ] **Step 5: Commit**

```bash
git add shared/config/config.go shared/config/config_test.go
git commit -m "feat: add config loader for write-service"
```

---

### Task 3: `shared/pgconn` — Postgres connection + migrations

**Files:**
- Test: `shared/pgconn/pgconn_test.go`
- Implementation (user writes this): `shared/pgconn/pgconn.go`
- Create (Claude writes these, not application logic): `infra/migrations/000001_init.up.sql`, `infra/migrations/000001_init.down.sql`

**Interfaces:**
- Consumes: `Config.DatabaseURL` from Task 2 (test uses a literal DSN instead, doesn't need `config` package).
- Produces: `func Open(databaseURL string) (*sql.DB, error)` and
  `func RunMigrations(databaseURL, migrationsDir string) error`. Task 4
  (`db.Repo`) calls both in its test setup; `cmd/write-service/main.go`
  (Task 7) calls both at startup.

- [ ] **Step 1: Claude writes the migration files**

`infra/migrations/000001_init.up.sql`:

```sql
CREATE TABLE pastes (
    paste_id   TEXT PRIMARY KEY,
    s3_key     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    size_bytes BIGINT NOT NULL,
    is_deleted BOOLEAN NOT NULL DEFAULT false,
    owner_id   TEXT
);
```

`infra/migrations/000001_init.down.sql`:

```sql
DROP TABLE pastes;
```

- [ ] **Step 2: Claude writes the failing test**

```go
package pgconn

import (
	"testing"
)

// localDSN matches infra/.env on this machine: POSTGRES_HOST_PORT=5433.
// If your infra/.env uses a different port (e.g. the 5432 default because
// nothing else was already bound to it on your machine), change this.
const localDSN = "postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable"


func TestOpenConnectsToRealPostgres(t *testing.T) {
	db, err := Open(localDSN)
	if err != nil {
		t.Skipf("local Postgres not reachable at %s (start it with `cd infra && docker compose up -d`, check infra/.env POSTGRES_HOST_PORT matches localDSN above): %v", localDSN, err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("Open() returned a DB that fails Ping(): %v", err)
	}
}

func TestRunMigrationsIsIdempotent(t *testing.T) {
	db, err := Open(localDSN)
	if err != nil {
		t.Skipf("local Postgres not reachable at %s: %v", localDSN, err)
	}
	db.Close()

	if err := RunMigrations(localDSN, "../../infra/migrations"); err != nil {
		t.Fatalf("RunMigrations() first run returned error: %v", err)
	}
	if err := RunMigrations(localDSN, "../../infra/migrations"); err != nil {
		t.Fatalf("RunMigrations() second run (should be a no-op) returned error: %v", err)
	}

	db2, err := Open(localDSN)
	if err != nil {
		t.Fatalf("Open() after RunMigrations returned error: %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.QueryRow("SELECT count(*) FROM pastes").Scan(&count); err != nil {
		t.Fatalf("pastes table not queryable after RunMigrations: %v", err)
	}
}
```

(Delete the stray `requirePostgres` line above before saving — it was left
in by mistake; `Open` itself in `TestOpenConnectsToRealPostgres` already
does the reachability check via its own error.)

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./shared/pgconn/... -v`
Expected: FAIL to compile — `undefined: Open` / `undefined: RunMigrations`
(and missing `github.com/jackc/pgx/v5` / `github.com/golang-migrate/migrate/v4`
dependencies).

- [ ] **Step 4: User writes `shared/pgconn/pgconn.go`**

Run `go get github.com/jackc/pgx/v5/stdlib github.com/golang-migrate/migrate/v4 github.com/golang-migrate/migrate/v4/database/postgres github.com/golang-migrate/migrate/v4/source/file` first.

`Open(databaseURL string) (*sql.DB, error)`: `import _
"github.com/jackc/pgx/v5/stdlib"`, then `sql.Open("pgx", databaseURL)`,
then `db.Ping()` to fail fast on a bad connection (return the ping error,
don't return a DB that can't actually connect).

`RunMigrations(databaseURL, migrationsDir string) error`: use
`golang-migrate`'s `migrate.New("file://"+migrationsDir, databaseURL)`,
then call `.Up()`. Treat `migrate.ErrNoChange` as success (that's what
makes the second call in the test a no-op instead of an error).

- [ ] **Step 5: User runs the test, confirms it passes**

Run: `go test ./shared/pgconn/... -v`
Expected: PASS (both test functions) — or SKIP if Postgres isn't reachable
(check `docker compose ps` and that `localDSN`'s port matches your
`infra/.env`'s `POSTGRES_HOST_PORT`).

- [ ] **Step 6: Commit**

```bash
git add shared/pgconn/pgconn.go shared/pgconn/pgconn_test.go infra/migrations/ go.mod go.sum
git commit -m "feat: add Postgres connection + migration runner"
```

---

### Task 4: `write-service/internal/db` — Repo

**Files:**
- Test: `write-service/internal/db/repo_test.go`
- Implementation (user writes this): `write-service/internal/db/repo.go`

**Interfaces:**
- Consumes: `pgconn.Open` and `pgconn.RunMigrations` from Task 3 (test setup only — `Repo` itself just takes a `*sql.DB`).
- Produces:
  ```go
  type Paste struct {
      ID        string
      S3Key     string
      CreatedAt time.Time
      ExpiresAt *time.Time
      SizeBytes int64
      IsDeleted bool
      OwnerID   *string
  }
  type Repo struct{ /* unexported *sql.DB field */ }
  func NewRepo(db *sql.DB) *Repo
  func (r *Repo) InsertPaste(ctx context.Context, p Paste) error
  func (r *Repo) Ping(ctx context.Context) error
  ```
  Task 6 (`handler`) depends on the `Repository` interface
  (`InsertPaste(ctx, db.Paste) error`), which `*Repo` satisfies. Task 7
  (`main.go`) constructs a `*Repo` via `NewRepo`.

- [ ] **Step 1: Claude writes the failing test**

```go
package db

import (
	"context"
	"testing"
	"time"

	"github.com/ah-naf/pastebin/shared/pgconn"
)

const localDSN = "postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable"

func setupRepo(t *testing.T) *Repo {
	t.Helper()
	if err := pgconn.RunMigrations(localDSN, "../../../infra/migrations"); err != nil {
		t.Skipf("could not apply migrations against %s (is `docker compose up -d` running? does infra/.env POSTGRES_HOST_PORT match?): %v", localDSN, err)
	}
	conn, err := pgconn.Open(localDSN)
	if err != nil {
		t.Skipf("local Postgres not reachable at %s: %v", localDSN, err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewRepo(conn)
}

func TestInsertPasteAndRetrieve(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	owner := "test-owner"
	expires := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	p := Paste{
		ID:        "test-insert-" + t.Name(),
		S3Key:     "test-insert-" + t.Name(),
		CreatedAt: time.Now().Truncate(time.Second).UTC(),
		ExpiresAt: &expires,
		SizeBytes: 42,
		IsDeleted: false,
		OwnerID:   &owner,
	}

	if err := repo.InsertPaste(ctx, p); err != nil {
		t.Fatalf("InsertPaste() returned error: %v", err)
	}

	// Duplicate ID must fail — the primary key is the second line of
	// defense behind shared/id's own uniqueness guarantee.
	if err := repo.InsertPaste(ctx, p); err == nil {
		t.Error("InsertPaste() with duplicate ID: expected error, got nil")
	}
}

func TestInsertPasteWithNilExpiresAtAndOwner(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	p := Paste{
		ID:        "test-nils-" + t.Name(),
		S3Key:     "test-nils-" + t.Name(),
		CreatedAt: time.Now().Truncate(time.Second).UTC(),
		ExpiresAt: nil,
		SizeBytes: 7,
		IsDeleted: false,
		OwnerID:   nil,
	}

	if err := repo.InsertPaste(ctx, p); err != nil {
		t.Fatalf("InsertPaste() with nil ExpiresAt/OwnerID returned error: %v", err)
	}
}

func TestRepoPing(t *testing.T) {
	repo := setupRepo(t)
	if err := repo.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./write-service/internal/db/... -v`
Expected: FAIL to compile — `undefined: Repo` / `undefined: NewRepo` / `undefined: Paste`.

- [ ] **Step 3: User writes `write-service/internal/db/repo.go`**

`InsertPaste` runs a plain `INSERT INTO pastes (paste_id, s3_key,
created_at, expires_at, size_bytes, is_deleted, owner_id) VALUES (...)`
with `db.ExecContext` — `ExpiresAt`/`OwnerID` are `*time.Time`/`*string`,
pass them straight through as query args (the pgx driver handles `nil`
pointer args as SQL `NULL` natively, no manual `sql.NullString`/`sql.NullTime`
needed). `Ping` runs `SELECT 1` with `db.PingContext(ctx)` or a trivial query.

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./write-service/internal/db/... -v`
Expected: PASS (all 3 test functions) — or SKIP if Postgres isn't reachable.

- [ ] **Step 5: Commit**

```bash
git add write-service/internal/db/repo.go write-service/internal/db/repo_test.go
git commit -m "feat: add Postgres repo for paste metadata"
```

---

### Task 5: `write-service/internal/storage` — Store

**Files:**
- Test: `write-service/internal/storage/store_test.go`
- Implementation (user writes this): `write-service/internal/storage/store.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (S3/MinIO credentials are literal test constants, matching Task 3-4's pattern of not depending on `shared/config` in tests).
- Produces:
  ```go
  func NewStore(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error)
  func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64) error
  func (s *Store) Ping(ctx context.Context) error
  ```
  Task 6 (`handler`) depends on the `Storer` interface
  (`Put(ctx, key, r, size) error`), which `*Store` satisfies. Task 7
  (`main.go`) constructs a `*Store` via `NewStore`.

- [ ] **Step 1: Claude writes the failing test**

```go
package storage

import (
	"bytes"
	"context"
	"testing"
)

// Matches infra/.env.example's MinIO defaults from Phase 0.
const (
	testEndpoint  = "localhost:9000"
	testAccessKey = "pastebin_minio"
	testSecretKey = "pastebin_minio_password"
)

func newTestStore(t *testing.T, bucket string) *Store {
	t.Helper()
	store, err := NewStore(context.Background(), testEndpoint, testAccessKey, testSecretKey, bucket, false)
	if err != nil {
		t.Skipf("local MinIO not reachable at %s (start it with `cd infra && docker compose up -d`): %v", testEndpoint, err)
	}
	return store
}

func TestNewStoreCreatesBucketIfMissing(t *testing.T) {
	// A throwaway bucket name proves auto-create works on a bucket that
	// does not exist yet.
	newTestStore(t, "pastebin-test-new-bucket")
}

func TestNewStoreSucceedsOnExistingBucket(t *testing.T) {
	// Calling NewStore twice against the same bucket must not error on
	// the second call (bucket-already-exists must be treated as success).
	newTestStore(t, "pastebin-test-existing-bucket")
	newTestStore(t, "pastebin-test-existing-bucket")
}

func TestPutUploadsObject(t *testing.T) {
	store := newTestStore(t, "pastebin-test-put")
	content := []byte("hello from the write-service test suite")

	err := store.Put(context.Background(), "test-key-put", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("Put() returned error: %v", err)
	}
}

func TestStorePing(t *testing.T) {
	store := newTestStore(t, "pastebin-test-ping")
	if err := store.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./write-service/internal/storage/... -v`
Expected: FAIL to compile — `undefined: NewStore` / `undefined: Store` (and
a missing `github.com/aws/aws-sdk-go-v2/...` dependency).

- [ ] **Step 3: User writes `write-service/internal/storage/store.go`**

Run `go get github.com/aws/aws-sdk-go-v2/aws github.com/aws/aws-sdk-go-v2/config github.com/aws/aws-sdk-go-v2/credentials github.com/aws/aws-sdk-go-v2/service/s3` first.

`NewStore` builds an `s3.Client` with a static credentials provider
(`credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")`), a
custom base endpoint pointing at `endpoint` (MinIO, not real AWS —
`s3.Options{BaseEndpoint: aws.String(scheme+"://"+endpoint), UsePathStyle:
true}`, where `scheme` is `"https"` if `useSSL` else `"http"`; MinIO needs
`UsePathStyle: true`), then calls `CreateBucket`. Treat a
`BucketAlreadyOwnedByYou` or `BucketAlreadyExists` API error as success,
not a failure — return any other error from `NewStore`.

`Put` calls `s3client.PutObject` with `Bucket`, `Key: key`, `Body: r`. `Ping`
calls `s3client.HeadBucket` on the stored bucket name.

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./write-service/internal/storage/... -v`
Expected: PASS (all 4 test functions) — or SKIP if MinIO isn't reachable.

- [ ] **Step 5: Commit**

```bash
git add write-service/internal/storage/store.go write-service/internal/storage/store_test.go go.mod go.sum
git commit -m "feat: add S3/MinIO storage client"
```

---

### Task 6: `write-service/internal/handler`

**Files:**
- Test: `write-service/internal/handler/handler_test.go`
- Implementation (user writes this): `write-service/internal/handler/handler.go`

**Interfaces:**
- Consumes: `db.Paste` (Task 4, only the type — tests use a fake
  `Repository`, not the real `*db.Repo`).
- Produces:
  ```go
  type IDGenerator interface { New() (string, error) }
  type Storer interface { Put(ctx context.Context, key string, r io.Reader, size int64) error }
  type Repository interface { InsertPaste(ctx context.Context, p db.Paste) error }
  type Pinger interface { Ping(context.Context) error }
  type Handler struct{ /* unexported fields */ }
  func New(gen IDGenerator, store Storer, repo Repository, baseURL string, maxBytes int64) *Handler
  func (h *Handler) CreatePaste(w http.ResponseWriter, r *http.Request)
  func Healthz(postgres Pinger, s3 Pinger) http.HandlerFunc
  ```
  `shared/id.Generator` satisfies `IDGenerator`; `*storage.Store` satisfies
  `Storer` and `Pinger`; `*db.Repo` satisfies `Repository` and `Pinger`.
  Task 7 (`main.go`) wires the real implementations in and registers both
  handler functions on the `http.ServeMux`.

- [ ] **Step 1: Claude writes the failing test**

```go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ah-naf/pastebin/write-service/internal/db"
)

type fakeGenerator struct {
	id  string
	err error
}

func (f *fakeGenerator) New() (string, error) { return f.id, f.err }

type fakeStore struct {
	err     error
	called  bool
	written []byte
}

func (f *fakeStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	b, _ := io.ReadAll(r)
	f.written = b
	return nil
}

type fakeRepo struct {
	err     error
	called  bool
	pastes  []db.Paste
}

func (f *fakeRepo) InsertPaste(ctx context.Context, p db.Paste) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	f.pastes = append(f.pastes, p)
	return nil
}

func doCreatePaste(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/paste", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreatePaste(rec, req)
	return rec
}

func TestCreatePasteHappyPath(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	store := &fakeStore{}
	repo := &fakeRepo{}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello world"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var resp struct {
		ID        string  `json:"id"`
		URL       string  `json:"url"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (body: %s)", err, rec.Body.String())
	}
	if resp.ID != "abc123" {
		t.Errorf("id = %q, want \"abc123\"", resp.ID)
	}
	if resp.URL != "http://localhost:8081/paste/abc123" {
		t.Errorf("url = %q, want \"http://localhost:8081/paste/abc123\"", resp.URL)
	}
	if resp.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want nil (no expires_in_seconds was sent)", *resp.ExpiresAt)
	}
	if string(store.written) != "hello world" {
		t.Errorf("content uploaded to store = %q, want \"hello world\"", store.written)
	}
	if !repo.called {
		t.Error("repo.InsertPaste was never called")
	}
}

func TestCreatePasteRejectsEmptyContent(t *testing.T) {
	h := New(&fakeGenerator{id: "x"}, &fakeStore{}, &fakeRepo{}, "http://localhost:8081", 1048576)
	rec := doCreatePaste(h, `{"content":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCreatePasteRejectsOversizedBody(t *testing.T) {
	h := New(&fakeGenerator{id: "x"}, &fakeStore{}, &fakeRepo{}, "http://localhost:8081", 10)
	big := `{"content":"this string is definitely longer than ten bytes"}`
	req := httptest.NewRequest(http.MethodPost, "/paste", bytes.NewReader([]byte(big)))
	rec := httptest.NewRecorder()
	h.CreatePaste(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestCreatePasteGeneratorErrorSkipsStoreAndRepo(t *testing.T) {
	gen := &fakeGenerator{err: errors.New("redis down")}
	store := &fakeStore{}
	repo := &fakeRepo{}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if store.called {
		t.Error("store.Put was called despite generator failing — must not upload without an ID")
	}
	if repo.called {
		t.Error("repo.InsertPaste was called despite generator failing")
	}
}

func TestCreatePasteStoreErrorSkipsRepo(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	store := &fakeStore{err: errors.New("s3 down")}
	repo := &fakeRepo{}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if repo.called {
		t.Error("repo.InsertPaste was called despite the S3 upload failing — violates upload-then-commit ordering")
	}
}

func TestCreatePasteRepoErrorDoesNotClaimSuccess(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	store := &fakeStore{}
	repo := &fakeRepo{err: errors.New("db down")}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello"}`)

	if rec.Code == http.StatusCreated {
		t.Error("status is 201 despite repo.InsertPaste failing — response must not claim success")
	}
	if !store.called {
		t.Error("store.Put should have been called (it succeeds) before repo failed")
	}
}

func TestCreatePasteWithExpiry(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	repo := &fakeRepo{}
	h := New(gen, &fakeStore{}, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello","expires_in_seconds":3600}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if len(repo.pastes) != 1 {
		t.Fatalf("expected 1 paste inserted, got %d", len(repo.pastes))
	}
	if repo.pastes[0].ExpiresAt == nil {
		t.Error("ExpiresAt is nil, want a non-nil time roughly 1 hour from now")
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

func TestHealthzS3Down(t *testing.T) {
	handlerFunc := Healthz(&fakePinger{}, &fakePinger{err: errors.New("down")})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handlerFunc(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./write-service/internal/handler/... -v`
Expected: FAIL to compile — `undefined: New` / `undefined: Handler` / `undefined: Healthz`.

- [ ] **Step 3: User writes `write-service/internal/handler/handler.go`**

`CreatePaste`: wrap `r.Body` with `http.MaxBytesReader(w, r.Body, h.maxBytes)`
before decoding JSON — a body over the limit makes `json.Decode` return an
error whose message contains "http: request body too large"; check for
that (or use `errors.As` against `*http.MaxBytesError`, available since Go
1.19) and respond `413`, not a generic `400`. Empty/whitespace-only
`content` → `400`. Otherwise: call `h.gen.New()` (error → `500`, return
before touching store/repo); call `h.store.Put(ctx, id, strings.NewReader(content), int64(len(content)))`
(error → `500`, return before touching repo); build a `db.Paste` (`ExpiresAt`
computed from `expires_in_seconds` if present and non-zero:
`time.Now().Add(time.Duration(n) * time.Second)`, else `nil`) and call
`h.repo.InsertPaste(ctx, paste)` (error → `500`); on full success, write
`201` with the JSON response shape from the test (`id`, `url` built as
`h.baseURL + "/paste/" + id`, `expires_at` as an RFC3339 string or `null`).

`Healthz(postgres, s3 Pinger) http.HandlerFunc`: returns a closure that
calls both `Ping`s with a short-timeout context (e.g. `context.WithTimeout`,
2 seconds), writes `200 {"status":"ok"}` if both succeed, `503
{"status":"degraded","postgres":"ok|error","s3":"ok|error"}` if either
fails.

- [ ] **Step 4: User runs the test, confirms it passes**

Run: `go test ./write-service/internal/handler/... -v`
Expected: PASS (all 10 test functions).

- [ ] **Step 5: Commit**

```bash
git add write-service/internal/handler/handler.go write-service/internal/handler/handler_test.go
git commit -m "feat: add write-service HTTP handlers"
```

---

### Task 7: `shared/cache` + `write-service/cmd/write-service/main.go`

No Go test file for this task — `main.go` is wiring, verified by actually
running the service and exercising it with `curl`, together.

**Files:**
- Implementation (user writes this): `shared/cache/redis.go`, `write-service/cmd/write-service/main.go`

**Interfaces:**
- Consumes: everything from Tasks 2-6 — `config.Load()`, `pgconn.Open`/`RunMigrations`,
  `id.NewGenerator`/`id.NewRedisCounterSource` (Phase 1), `storage.NewStore`,
  `db.NewRepo`, `handler.New`/`handler.Healthz`.
- Produces: a running HTTP server on `cfg.Port` with `POST /paste` and
  `GET /healthz` registered.

- [ ] **Step 1: User writes `shared/cache/redis.go`**

```go
package cache

// func NewClient(addr string) *redis.Client — one line, wraps
// redis.NewClient(&redis.Options{Addr: addr}). No test needed (trivial
// passthrough of a well-tested library constructor).
```

- [ ] **Step 2: User writes `write-service/cmd/write-service/main.go`**

Order: `config.Load()` (fatal on error) → `pgconn.Open(cfg.DatabaseURL)`
(fatal on error) → `pgconn.RunMigrations(cfg.DatabaseURL, "infra/migrations")`
(fatal on error — note the path is relative to wherever the binary runs
from, `infra/migrations`, not `../../infra/migrations` like the tests) →
`cache.NewClient(cfg.RedisAddr)` → `id.NewRedisCounterSource(redisClient)`
→ `id.NewGenerator(cfg.IDXORSecret, counterSource)` →
`storage.NewStore(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey,
cfg.S3Bucket, cfg.S3UseSSL)` (fatal on error) → `db.NewRepo(sqlDB)` →
`handler.New(generator, store, repo, cfg.PublicBaseURL, cfg.MaxPasteBytes)`.
Register `mux.HandleFunc("POST /paste", h.CreatePaste)` and
`mux.HandleFunc("GET /healthz", handler.Healthz(repo, store))` (Go 1.22+
`ServeMux` method-prefixed patterns). `http.ListenAndServe(":"+cfg.Port, mux)`.

- [ ] **Step 3: User runs the service and both of you verify with curl**

```bash
cd infra && docker compose up -d && cd ..
export DATABASE_URL="postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable"
export S3_ACCESS_KEY=pastebin_minio
export S3_SECRET_KEY=pastebin_minio_password
export ID_XOR_SECRET=9f3a1c2e5b7d0f14
go run ./write-service/cmd/write-service
```

In another terminal:

```bash
curl -i http://localhost:8080/healthz
curl -i -X POST http://localhost:8080/paste -d '{"content":"hello from curl"}'
curl -i -X POST http://localhost:8080/paste -d '{"content":"expires soon","expires_in_seconds":60}'
curl -i -X POST http://localhost:8080/paste -d '{"content":""}'
```

Expected: `/healthz` → `200 {"status":"ok"}`. First two POSTs → `201` with
`id`/`url`/`expires_at` (null for the first, a timestamp for the second).
Empty-content POST → `400`.

Then confirm the data actually landed:

```bash
docker compose -f infra/docker-compose.yml exec postgres psql -U pastebin -d pastebin -c "SELECT paste_id, s3_key, expires_at FROM pastes;"
```

Expected: two rows matching the two successful curl calls.

- [ ] **Step 4: Commit**

```bash
git add shared/cache/redis.go write-service/cmd/write-service/main.go go.mod go.sum
git commit -m "feat: wire up write-service main"
```

---

## Phase 2 done-criteria checklist

- [ ] `go test ./...` passes (Redis/Postgres/MinIO-dependent tests pass or skip cleanly).
- [ ] `docker compose ps` shows all three Phase 0 services `(healthy)`, with Postgres now reachable from the host at the port in `infra/.env`.
- [ ] `go run ./write-service/cmd/write-service` starts and serves `GET /healthz` → `200`.
- [ ] `POST /paste` with valid JSON content returns `201` with a shareable URL, uploads to MinIO, and inserts a row in Postgres — verified by hand with curl + `psql` per Task 7.
- [ ] `POST /paste` with empty content → `400`; oversized body → `413`.
- [ ] No code path writes a metadata row before its S3 upload has succeeded.

Once checked, Phase 2 is done. Next: Phase 3 (Read Service — `GET /paste/{id}` via cache → DB → S3, with negative caching).
