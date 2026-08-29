package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ah-naf/pastebin/shared/pgconn"
)

const localDSN = "postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable"

// setupRepo returns a Repo plus the raw *sql.DB so tests can clean up the
// rows they insert (paste_id values below are deterministic per test name,
// so a rerun without cleanup collides with the previous run's leftover row).
func setupRepo(t *testing.T) (*Repo, *sql.DB) {
	t.Helper()
	if err := pgconn.RunMigrations(localDSN, "../../../infra/migrations"); err != nil {
		t.Skipf("could not apply migrations against %s (is `docker compose up -d` running? does infra/.env POSTGRES_HOST_PORT match?): %v", localDSN, err)
	}
	conn, err := pgconn.Open(localDSN)
	if err != nil {
		t.Skipf("local Postgres not reachable at %s: %v", localDSN, err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewRepo(conn), conn
}

func deletePaste(t *testing.T, conn *sql.DB, id string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := conn.Exec(`DELETE FROM pastes WHERE paste_id = $1`, id); err != nil {
			t.Errorf("cleanup: failed to delete test row %q: %v", id, err)
		}
	})
}

func TestInsertPasteAndRetrieve(t *testing.T) {
	repo, conn := setupRepo(t)
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
	deletePaste(t, conn, p.ID)

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
	repo, conn := setupRepo(t)
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
	deletePaste(t, conn, p.ID)

	if err := repo.InsertPaste(ctx, p); err != nil {
		t.Fatalf("InsertPaste() with nil ExpiresAt/OwnerID returned error: %v", err)
	}
}

func TestRepoPing(t *testing.T) {
	repo, _ := setupRepo(t)
	if err := repo.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
