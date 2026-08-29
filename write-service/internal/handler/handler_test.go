package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ah-naf/pastebin/write-service/internal/db"
)

type fakeGenerator struct {
	id  string
	err error
}

func (f *fakeGenerator) New() (string, error) { return f.id, f.err }

type fakeStore struct {
	err     error
	called  bool
	written []byte
}

func (f *fakeStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	b, _ := io.ReadAll(r)
	f.written = b
	return nil
}

type fakeRepo struct {
	err    error
	called bool
	pastes []db.Paste
}

func (f *fakeRepo) InsertPaste(ctx context.Context, p db.Paste) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	f.pastes = append(f.pastes, p)
	return nil
}

func doCreatePaste(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/paste", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreatePaste(rec, req)
	return rec
}

func TestCreatePasteHappyPath(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	store := &fakeStore{}
	repo := &fakeRepo{}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello world"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var resp struct {
		ID        string  `json:"id"`
		URL       string  `json:"url"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (body: %s)", err, rec.Body.String())
	}
	if resp.ID != "abc123" {
		t.Errorf("id = %q, want \"abc123\"", resp.ID)
	}
	if resp.URL != "http://localhost:8081/paste/abc123" {
		t.Errorf("url = %q, want \"http://localhost:8081/paste/abc123\"", resp.URL)
	}
	if resp.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want nil (no expires_in_seconds was sent)", *resp.ExpiresAt)
	}
	if string(store.written) != "hello world" {
		t.Errorf("content uploaded to store = %q, want \"hello world\"", store.written)
	}
	if !repo.called {
		t.Error("repo.InsertPaste was never called")
	}
}

func TestCreatePasteRejectsEmptyContent(t *testing.T) {
	h := New(&fakeGenerator{id: "x"}, &fakeStore{}, &fakeRepo{}, "http://localhost:8081", 1048576)
	rec := doCreatePaste(h, `{"content":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCreatePasteRejectsOversizedBody(t *testing.T) {
	h := New(&fakeGenerator{id: "x"}, &fakeStore{}, &fakeRepo{}, "http://localhost:8081", 10)
	big := `{"content":"this string is definitely longer than ten bytes"}`
	req := httptest.NewRequest(http.MethodPost, "/paste", bytes.NewReader([]byte(big)))
	rec := httptest.NewRecorder()
	h.CreatePaste(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestCreatePasteGeneratorErrorSkipsStoreAndRepo(t *testing.T) {
	gen := &fakeGenerator{err: errors.New("redis down")}
	store := &fakeStore{}
	repo := &fakeRepo{}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if store.called {
		t.Error("store.Put was called despite generator failing — must not upload without an ID")
	}
	if repo.called {
		t.Error("repo.InsertPaste was called despite generator failing")
	}
}

func TestCreatePasteStoreErrorSkipsRepo(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	store := &fakeStore{err: errors.New("s3 down")}
	repo := &fakeRepo{}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if repo.called {
		t.Error("repo.InsertPaste was called despite the S3 upload failing — violates upload-then-commit ordering")
	}
}

func TestCreatePasteRepoErrorDoesNotClaimSuccess(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	store := &fakeStore{}
	repo := &fakeRepo{err: errors.New("db down")}
	h := New(gen, store, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello"}`)

	if rec.Code == http.StatusCreated {
		t.Error("status is 201 despite repo.InsertPaste failing — response must not claim success")
	}
	if !store.called {
		t.Error("store.Put should have been called (it succeeds) before repo failed")
	}
}

func TestCreatePasteWithExpiry(t *testing.T) {
	gen := &fakeGenerator{id: "abc123"}
	repo := &fakeRepo{}
	h := New(gen, &fakeStore{}, repo, "http://localhost:8081", 1048576)

	rec := doCreatePaste(h, `{"content":"hello","expires_in_seconds":3600}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if len(repo.pastes) != 1 {
		t.Fatalf("expected 1 paste inserted, got %d", len(repo.pastes))
	}
	if repo.pastes[0].ExpiresAt == nil {
		t.Error("ExpiresAt is nil, want a non-nil time roughly 1 hour from now")
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

func TestHealthzS3Down(t *testing.T) {
	handlerFunc := Healthz(&fakePinger{}, &fakePinger{err: errors.New("down")})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handlerFunc(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
