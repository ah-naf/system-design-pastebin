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
