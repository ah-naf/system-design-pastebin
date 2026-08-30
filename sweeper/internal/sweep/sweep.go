package sweep

import (
	"context"
	"log"

	"github.com/ah-naf/pastebin/sweeper/internal/db"
)

type Repository interface {
	FindExpiredBatch(ctx context.Context, limit int) ([]db.ExpiredPaste, error)
	DeleteMetadata(ctx context.Context, id string) error
}

type Deleter interface {
	Delete(ctx context.Context, key string) error
}

func Run(ctx context.Context, repo Repository, deleter Deleter, batchSize int) (int, error) {
	count := 0

	for {
		expiredPastes, err := repo.FindExpiredBatch(ctx, batchSize)
		if err != nil {
			return count, err
		}

		if len(expiredPastes) == 0 {
			break
		}

		for _, paste := range expiredPastes {
			if err := repo.DeleteMetadata(ctx, paste.ID); err != nil {
				log.Printf("deleting metadata failed for %s, skipping: %v", paste.ID, err)
				continue
			}
			count++

			if err := deleter.Delete(ctx, paste.S3Key); err != nil {
				log.Printf("deleting S3 object failed for %s, skipping: %v", paste.S3Key, err)
				continue
			}
		}
	}

	return count, nil
}
