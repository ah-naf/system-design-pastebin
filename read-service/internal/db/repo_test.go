package db

import (
	"context"
	"database/sql"
	"errors"
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
		if _, err := conn.Exec(`DELETE FROM pastes WHERE paste_id = $1`, id); err != nil {
			t.Errorf("cleanup: failed to delete test row %q: %v", id, err)
		}
	})
}

func TestGetPasteFindsValidRow(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-valid-" + t.Name()
	insertRow(t, conn, id, false, nil)

	meta, err := repo.GetPaste(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPaste() returned error: %v", err)
	}
	if meta.S3Key != id {
		t.Errorf("S3Key = %q, want %q", meta.S3Key, id)
	}
	if meta.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil", *meta.ExpiresAt)
	}
}

func TestGetPasteFindsRowWithFutureExpiry(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-future-" + t.Name()
	future := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	insertRow(t, conn, id, false, &future)

	meta, err := repo.GetPaste(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPaste() returned error: %v", err)
	}
	if meta.ExpiresAt == nil || !meta.ExpiresAt.Equal(future) {
		t.Errorf("ExpiresAt = %v, want %v", meta.ExpiresAt, future)
	}
}

func TestGetPasteRejectsExpiredRow(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-expired-" + t.Name()
	past := time.Now().Add(-time.Hour).Truncate(time.Second).UTC()
	insertRow(t, conn, id, false, &past)

	_, err := repo.GetPaste(context.Background(), id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPaste() on expired row: err = %v, want ErrNotFound", err)
	}
}

func TestGetPasteRejectsDeletedRow(t *testing.T) {
	repo, conn := setupRepo(t)
	id := "test-deleted-" + t.Name()
	insertRow(t, conn, id, true, nil)

	_, err := repo.GetPaste(context.Background(), id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPaste() on deleted row: err = %v, want ErrNotFound", err)
	}
}

func TestGetPasteRejectsMissingRow(t *testing.T) {
	repo, _ := setupRepo(t)
	_, err := repo.GetPaste(context.Background(), "does-not-exist-"+t.Name())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPaste() on missing row: err = %v, want ErrNotFound", err)
	}
}

func TestRepoPing(t *testing.T) {
	repo, _ := setupRepo(t)
	if err := repo.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
