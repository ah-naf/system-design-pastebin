package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ah-naf/pastebin/shared/pgconn"
)

const localDSN = "postgres://pastebin:pastebin_dev_password@localhost:5433/pastebin?sslmode=disable"

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

func insertRow(t *testing.T, conn *sql.DB, id string, isDeleted bool, expiresAt *time.Time) {
	t.Helper()
	_, err := conn.Exec(
		`INSERT INTO pastes (paste_id, s3_key, created_at, expires_at, size_bytes, is_deleted)
		 VALUES ($1, $1, now(), $2, 10, $3)`,
		id, expiresAt, isDeleted,
	)
	if err != nil {
		t.Fatalf("test setup: failed to insert row %q: %v", id, err)
	}
	t.Cleanup(func() {
		conn.Exec(`DELETE FROM pastes WHERE paste_id = $1`, id)
	})
}

func TestFindExpiredBatchReturnsOnlyExpiredNonDeletedRows(t *testing.T) {
	repo, conn := setupRepo(t)
	past := time.Now().Add(-time.Hour).Truncate(time.Second).UTC()
	future := time.Now().Add(time.Hour).Truncate(time.Second).UTC()

	expiredID := "test-expired-" + t.Name()
	futureID := "test-future-" + t.Name()
	deletedID := "test-deleted-" + t.Name()
	neverExpiresID := "test-never-" + t.Name()

	insertRow(t, conn, expiredID, false, &past)
	insertRow(t, conn, futureID, false, &future)
	insertRow(t, conn, deletedID, true, &past)
	insertRow(t, conn, neverExpiresID, false, nil)

	batch, err := repo.FindExpiredBatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("FindExpiredBatch() returned error: %v", err)
	}

	found := make(map[string]bool)
	for _, p := range batch {
		found[p.ID] = true
	}
	if !found[expiredID] {
		t.Errorf("expired row %q missing from batch", expiredID)
	}
	if found[futureID] {
		t.Errorf("future-expiry row %q should not be in batch", futureID)
	}
	if found[deletedID] {
		t.Errorf("already-deleted row %q should not be in batch", deletedID)
	}
	if found[neverExpiresID] {
		t.Errorf("never-expiring row %q should not be in batch", neverExpiresID)
	}
}

func TestFindExpiredBatchRespectsLimit(t *testing.T) {
	repo, conn := setupRepo(t)
	past := time.Now().Add(-time.Hour).Truncate(time.Second).UTC()
	for i := 0; i < 3; i++ {
		insertRow(t, conn, "test-limit-"+t.Name()+"-"+string(rune('a'+i)), false, &past)
	}

	batch, err := repo.FindExpiredBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("FindExpiredBatch() returned error: %v", err)
	}
	if len(batch) > 2 {
		t.Errorf("batch length = %d, want <= 2 (limit)", len(batch))
	}
}

func TestDeleteMetadataRemovesRow(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-delete-" + t.Name()
	insertRow(t, conn, id, false, nil)

	if err := repo.DeleteMetadata(context.Background(), id); err != nil {
		t.Fatalf("DeleteMetadata() returned error: %v", err)
	}

	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM pastes WHERE paste_id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("could not verify deletion: %v", err)
	}
	if count != 0 {
		t.Errorf("row still exists after DeleteMetadata(), count = %d", count)
	}
}

func TestDeleteMetadataOnMissingRowDoesNotError(t *testing.T) {
	repo, _ := setupRepo(t)
	if err := repo.DeleteMetadata(context.Background(), "does-not-exist-"+t.Name()); err != nil {
		t.Errorf("DeleteMetadata() on missing row returned error: %v, want nil", err)
	}
}
