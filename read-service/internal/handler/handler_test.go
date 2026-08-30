package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ah-naf/pastebin/read-service/internal/cache"
	"github.com/ah-naf/pastebin/read-service/internal/db"
)

type fakeCacheGetter struct {
	content []byte
	result  cache.Result
}

func (f *fakeCacheGetter) Get(ctx context.Context, id string) ([]byte, cache.Result) {
	return f.content, f.result
}

type fakeCacheSetter struct {
	positiveCalled bool
	positiveTTL    time.Duration
	negativeCalled bool
}

func (f *fakeCacheSetter) SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration) {
	f.positiveCalled = true
	f.positiveTTL = ttl
}

func (f *fakeCacheSetter) SetNegative(ctx context.Context, id string, ttl time.Duration) {
	f.negativeCalled = true
}

type fakeRepo struct {
	meta *db.PasteMeta
	err  error
}

func (f *fakeRepo) GetPaste(ctx context.Context, id string) (*db.PasteMeta, error) {
	return f.meta, f.err
}

type fakeStore struct {
	content string
	err     error
	called  bool
}

func (f *fakeStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	f.called = true
	if f.err != nil {
		return nil, 0, f.err
	}
	return io.NopCloser(strings.NewReader(f.content)), int64(len(f.content)), nil
}

func doGetPaste(h *Handler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/paste/"+id, nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.GetPaste(rec, req)
	return rec
}

func TestGetPasteCacheHitSkipsRepoAndStore(t *testing.T) {
	cacheGet := &fakeCacheGetter{content: []byte("cached content"), result: cache.Hit}
	repo := &fakeRepo{err: errors.New("repo must not be called on cache hit")}
	store := &fakeStore{err: errors.New("store must not be called on cache hit")}
	h := New(cacheGet, &fakeCacheSetter{}, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "cached content" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "cached content")
	}
	if store.called {
		t.Error("store.Get was called despite cache hit")
	}
}

func TestGetPasteCacheNegativeReturns404WithoutRepoOrStore(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Negative}
	repo := &fakeRepo{err: errors.New("repo must not be called on negative cache hit")}
	store := &fakeStore{err: errors.New("store must not be called on negative cache hit")}
	h := New(cacheGet, &fakeCacheSetter{}, repo, store)

	rec := doGetPaste(h, "missing123")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if store.called {
		t.Error("store.Get was called despite negative cache hit")
	}
}

func TestGetPasteCacheMissRepoNotFoundSetsNegative(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{err: db.ErrNotFound}
	h := New(cacheGet, cacheSet, repo, &fakeStore{})

	rec := doGetPaste(h, "gone123")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !cacheSet.negativeCalled {
		t.Error("cache.SetNegative was not called after repo returned ErrNotFound")
	}
}

func TestGetPasteCacheMissRepoErrorReturns500(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	cacheSet := &fakeCacheSetter{}
	repo := &fakeRepo{err: errors.New("db down")}
	h := New(cacheGet, cacheSet, repo, &fakeStore{})

	rec := doGetPaste(h, "x")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if cacheSet.negativeCalled {
		t.Error("cache.SetNegative was called for a real DB error, not just ErrNotFound")
	}
}

func TestGetPasteCacheMissRepoOKStoreErrorReturns500(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	repo := &fakeRepo{meta: &db.PasteMeta{S3Key: "abc123"}}
	store := &fakeStore{err: errors.New("s3 down")}
	h := New(cacheGet, &fakeCacheSetter{}, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestGetPasteFullMissPopulatesCache(t *testing.T) {
	cacheGet := &fakeCacheGetter{result: cache.Miss}
	cacheSet := &fakeCacheSetter{}
	expires := time.Now().Add(30 * time.Minute)
	repo := &fakeRepo{meta: &db.PasteMeta{S3Key: "abc123", ExpiresAt: &expires}}
	store := &fakeStore{content: "hello from S3"}
	h := New(cacheGet, cacheSet, repo, store)

	rec := doGetPaste(h, "abc123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "hello from S3" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello from S3")
	}
	if !cacheSet.positiveCalled {
		t.Fatal("cache.SetPositive was not called after a full cache miss")
	}
	if cacheSet.positiveTTL <= 0 || cacheSet.positiveTTL > time.Hour {
		t.Errorf("positive TTL = %v, want > 0 and <= 1h (bounded by expires_at)", cacheSet.positiveTTL)
	}
}

func TestGetPasteContentTypeIsPlainText(t *testing.T) {
	cacheGet := &fakeCacheGetter{content: []byte("x"), result: cache.Hit}
	h := New(cacheGet, &fakeCacheSetter{}, &fakeRepo{}, &fakeStore{})

	rec := doGetPaste(h, "x")

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want prefix \"text/plain\"", ct)
	}
}

type fakePinger struct{ err error }

func (f *fakePinger) Ping(ctx context.Context) error { return f.err }

func TestHealthzBothUp(t *testing.T) {
	handlerFunc := Healthz(&fakePinger{}, &fakePinger{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handlerFunc(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHealthzPostgresDown(t *testing.T) {
	handlerFunc := Healthz(&fakePinger{err: errors.New("down")}, &fakePinger{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handlerFunc(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
