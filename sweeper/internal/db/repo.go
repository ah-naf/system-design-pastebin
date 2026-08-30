package db

import (
	"context"
	"database/sql"
)

type Repo struct {
	db *sql.DB
}

type ExpiredPaste struct {
	ID    string
	S3Key string
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{
		db: db,
	}
}

func (r *Repo) FindExpiredBatch(ctx context.Context, limit int) ([]ExpiredPaste, error) {
	query := `
	SELECT paste_id, s3_key FROM pastes
	WHERE (expires_at IS NOT NULL AND expires_at <= now() AND is_deleted = false)
	ORDER BY expires_at ASC
	LIMIT $1
	`

	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	expiredPastes := make([]ExpiredPaste, 0)
	for rows.Next() {
		var paste ExpiredPaste
		err = rows.Scan(&paste.ID, &paste.S3Key)
		if err != nil {
			return nil, err
		}

		expiredPastes = append(expiredPastes, paste)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return expiredPastes, nil
}

func (r *Repo) DeleteMetadata(ctx context.Context, id string) error {
	query := `
	DELETE FROM pastes WHERE paste_id = $1
	`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}
