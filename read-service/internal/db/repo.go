package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("paste not found")

type Repo struct {
	db *sql.DB
}

type PasteMeta struct {
	S3Key     string
	ExpiresAt *time.Time
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{
		db: db,
	}
}

func (r *Repo) GetPaste(ctx context.Context, id string) (*PasteMeta, error) {
	query := `
	SELECT s3_key, expires_at FROM pastes 
	WHERE paste_id = $1
	AND is_deleted = false
	AND (expires_at IS NULL OR expires_at > NOW())
	`

	row := r.db.QueryRowContext(
		ctx,
		query,
		id,
	)

	var meta PasteMeta

	err := row.Scan(&meta.S3Key, &meta.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}

		return nil, err
	}

	return &meta, nil
}

func (r *Repo) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}
