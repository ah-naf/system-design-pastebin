package db

import (
	"context"
	"database/sql"
	"time"
)

type Repo struct {
	db *sql.DB
}

type Paste struct {
	ID        string
	S3Key     string
	CreatedAt time.Time
	ExpiresAt *time.Time
	SizeBytes int64
	IsDeleted bool
	OwnerID   *string
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{
		db: db,
	}
}

func (r *Repo) InsertPaste(ctx context.Context, p Paste) error {
	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO pastes (paste_id, s3_key, created_at, expires_at, size_bytes, is_deleted, owner_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.ID, p.S3Key, p.CreatedAt, p.ExpiresAt, p.SizeBytes, p.IsDeleted, p.OwnerID,
	)

	return err
}

func (r *Repo) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}
