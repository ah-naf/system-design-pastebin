package sweep

import (
	"context"
	"errors"
	"testing"

	"github.com/ah-naf/pastebin/sweeper/internal/db"
)

type fakeRepo struct {
	batches   [][]db.ExpiredPaste
	callIndex int
	deleteErr map[string]error // paste id -> error to return from DeleteMetadata
	findErr   error
}

func (f *fakeRepo) FindExpiredBatch(ctx context.Context, limit int) ([]db.ExpiredPaste, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	if f.callIndex >= len(f.batches) {
		return nil, nil
	}
	batch := f.batches[f.callIndex]
	f.callIndex++
	return batch, nil
}

func (f *fakeRepo) DeleteMetadata(ctx context.Context, id string) error {
	if err, ok := f.deleteErr[id]; ok {
		return err
	}
	return nil
}

type fakeDeleter struct {
	deletedKeys []string
	deleteErr   map[string]error
}

func (f *fakeDeleter) Delete(ctx context.Context, key string) error {
	if err, ok := f.deleteErr[key]; ok {
		return err
	}
	f.deletedKeys = append(f.deletedKeys, key)
	return nil
}

func TestRunEmptyBatchDeletesNothing(t *testing.T) {
	repo := &fakeRepo{batches: [][]db.ExpiredPaste{{}}}
	store := &fakeDeleter{}

	deleted, err := Run(context.Background(), repo, store, 10)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

func TestRunProcessesMultipleBatches(t *testing.T) {
	repo := &fakeRepo{
		batches: [][]db.ExpiredPaste{
			{{ID: "a", S3Key: "a"}, {ID: "b", S3Key: "b"}},
			{{ID: "c", S3Key: "c"}},
			{},
		},
	}
	store := &fakeDeleter{}

	deleted, err := Run(context.Background(), repo, store, 2)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}
	if len(store.deletedKeys) != 3 {
		t.Errorf("S3 objects deleted = %d, want 3", len(store.deletedKeys))
	}
}

func TestRunSkipsS3DeleteWhenMetadataDeleteFails(t *testing.T) {
	repo := &fakeRepo{
		batches: [][]db.ExpiredPaste{
			{{ID: "a", S3Key: "a"}, {ID: "b", S3Key: "b"}},
			{},
		},
		deleteErr: map[string]error{"a": errors.New("db error on a")},
	}
	store := &fakeDeleter{}

	deleted, err := Run(context.Background(), repo, store, 10)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (only b)", deleted)
	}
	for _, k := range store.deletedKeys {
		if k == "a" {
			t.Error("S3 object \"a\" was deleted despite its metadata delete failing")
		}
	}
}

func TestRunCountsRowAsDeletedEvenIfS3DeleteFails(t *testing.T) {
	repo := &fakeRepo{
		batches: [][]db.ExpiredPaste{
			{{ID: "a", S3Key: "a"}},
			{},
		},
	}
	store := &fakeDeleter{deleteErr: map[string]error{"a": errors.New("s3 error")}}

	deleted, err := Run(context.Background(), repo, store, 10)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (metadata delete succeeded even though S3 delete failed)", deleted)
	}
}

func TestRunStopsOnFindExpiredBatchError(t *testing.T) {
	wantErr := errors.New("db unreachable")
	repo := &fakeRepo{findErr: wantErr}
	store := &fakeDeleter{}

	_, err := Run(context.Background(), repo, store, 10)
	if !errors.Is(err, wantErr) {
		t.Errorf("Run() error = %v, want %v", err, wantErr)
	}
}
