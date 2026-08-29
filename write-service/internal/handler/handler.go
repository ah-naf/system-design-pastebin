package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ah-naf/pastebin/write-service/internal/db"
)

type IDGenerator interface {
	New() (string, error)
}

type Storer interface {
	Put(ctx context.Context, key string, r io.Reader, size int64) error
}

type Repository interface {
	InsertPaste(ctx context.Context, p db.Paste) error
}

type Pinger interface {
	Ping(ctx context.Context) error
}

type Handler struct {
	generator IDGenerator
	store     Storer
	repo      Repository
	baseURL   string
	maxBytes  int64
}

type createPasteRequest struct {
	Content          string `json:"content"`
	ExpiresInSeconds *int64 `json:"expires_in_seconds,omitempty"`
}

type createPasteResponse struct {
	ID        string  `json:"id"`
	URL       string  `json:"url"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

func New(generator IDGenerator, store Storer, repo Repository, baseURL string, maxBytes int64) *Handler {
	return &Handler{
		generator: generator,
		store:     store,
		repo:      repo,
		baseURL:   baseURL,
		maxBytes:  maxBytes,
	}
}

func (h *Handler) CreatePaste(res http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(res, req.Body, h.maxBytes)

	var input createPasteRequest

	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(res, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}

		http.Error(res, "invalid request body", http.StatusBadRequest)
		return
	}

	if input.Content == "" {
		http.Error(res, "content is required", http.StatusBadRequest)
		return
	}

	id, err := h.generator.New()
	if err != nil {
		http.Error(res, "failed to generate paste ID", http.StatusInternalServerError)
		return
	}

	var expiresAt *time.Time

	if input.ExpiresInSeconds != nil {
		if *input.ExpiresInSeconds < 0 {
			http.Error(res, "expires_in_seconds must be greater than 0", http.StatusBadRequest)
			return
		}

		t := time.Now().UTC().Add(
			time.Duration(*input.ExpiresInSeconds) * time.Second,
		)
		expiresAt = &t
	}

	key := id
	content := []byte(input.Content)

	if err := h.store.Put(
		req.Context(),
		key,
		strings.NewReader(input.Content),
		int64(len(content)),
	); err != nil {
		http.Error(res, "failed to store paste", http.StatusInternalServerError)
		return
	}

	paste := db.Paste{
		ID:        id,
		S3Key:     key,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
		SizeBytes: int64(len(content)),
		IsDeleted: false,
		OwnerID:   nil,
	}

	if err := h.repo.InsertPaste(req.Context(), paste); err != nil {
		http.Error(res, "failed to save paste", http.StatusInternalServerError)
		return
	}

	var expiresAtString *string
	if expiresAt != nil {
		value := expiresAt.Format(time.RFC3339)
		expiresAtString = &value
	}

	response := createPasteResponse{
		ID:        id,
		URL:       fmt.Sprintf("%s/paste/%s", strings.TrimRight(h.baseURL, "/"), id),
		ExpiresAt: expiresAtString,
	}

	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(http.StatusCreated)

	json.NewEncoder(res).Encode(response)
}

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
