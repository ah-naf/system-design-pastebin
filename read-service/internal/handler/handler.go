package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ah-naf/pastebin/read-service/internal/cache"
	"github.com/ah-naf/pastebin/read-service/internal/db"
)

type CacheGetter interface {
	Get(ctx context.Context, id string) ([]byte, cache.Result)
}

type CacheSetter interface {
	SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration)
	SetNegative(ctx context.Context, id string, ttl time.Duration)
}

type Repository interface {
	GetPaste(ctx context.Context, id string) (*db.PasteMeta, error)
}

type Pinger interface {
	Ping(ctx context.Context) error
}

type StoreRepository interface {
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

type Handler struct {
	cacheGetter CacheGetter
	cacheSetter CacheSetter
	repo        Repository
	storeRepo   StoreRepository
}

func New(cacheGetter CacheGetter, cacheSetter CacheSetter, repo Repository, storeRepo StoreRepository) *Handler {
	return &Handler{
		cacheGetter: cacheGetter,
		cacheSetter: cacheSetter,
		repo:        repo,
		storeRepo:   storeRepo,
	}
}

func (h *Handler) GetPaste(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	content, result := h.cacheGetter.Get(ctx, id)
	switch result {
	case cache.Hit:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
		return
	case cache.Negative:
		http.NotFound(w, r)
		return
	case cache.Miss:
		// Continue to db
	}

	meta, err := h.repo.GetPaste(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			h.cacheSetter.SetNegative(ctx, id, 1*time.Minute)
			http.NotFound(w, r)
			return
		}

		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	body, size, err := h.storeRepo.Get(ctx, meta.S3Key)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", formatContentLength(size))
	w.WriteHeader(http.StatusOK)

	var buf bytes.Buffer
	tee := io.TeeReader(body, &buf)

	_, err = io.Copy(w, tee)
	if err != nil {
		return
	}

	ttl := 1 * time.Hour
	if meta.ExpiresAt != nil {
		remaining := time.Until(*meta.ExpiresAt)
		if remaining <= 0 {
			return
		}

		if remaining < ttl {
			ttl = remaining
		}
	}

	h.cacheSetter.SetPositive(ctx, id, buf.Bytes(), ttl)
}

func formatContentLength(size int64) string { return strconv.FormatInt(size, 10) }

func Healthz(postgres Pinger, s3 Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := postgres.Ping(ctx); err != nil {
			http.Error(w, "postgres unavailable", http.StatusServiceUnavailable)
			return
		}

		if err := s3.Ping(ctx); err != nil {
			http.Error(w, "s3 unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
	}
}
