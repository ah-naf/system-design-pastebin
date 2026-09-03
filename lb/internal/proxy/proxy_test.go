package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ah-naf/pastebin/lb/internal/pool"
)

func TestServeHTTPProxiesToHealthyBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From-Backend", "yes")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from backend"))
	}))
	defer backend.Close()

	p := pool.New([]string{backend.URL})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "hello from backend" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello from backend")
	}
	if rec.Header().Get("X-From-Backend") != "yes" {
		t.Error("backend response header was not forwarded through the proxy")
	}
}

func TestServeHTTPRetriesOnUnreachableBackend(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("from the healthy one"))
	}))
	defer healthy.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := dead.URL
	dead.Close() // nobody is listening at deadAddr now

	p := pool.New([]string{deadAddr, healthy.URL})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != "from the healthy one" {
		t.Errorf("body = %q, want %q (should have retried onto the healthy backend)", rec.Body.String(), "from the healthy one")
	}
}

func TestServeHTTPReturns502WhenAllBackendsUnreachable(t *testing.T) {
	dead1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead1Addr := dead1.URL
	dead1.Close()

	dead2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead2Addr := dead2.URL
	dead2.Close()

	p := pool.New([]string{dead1Addr, dead2Addr})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestServeHTTPReturns503WhenNoHealthyBackend(t *testing.T) {
	empty := pool.New(nil)
	proxy := New(empty)

	req := httptest.NewRequest(http.MethodGet, "/paste/abc", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestServeHTTPForwardsRequestBodyOnRetry(t *testing.T) {
	var receivedBody string
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := dead.URL
	dead.Close()

	p := pool.New([]string{deadAddr, healthy.URL})
	proxy := New(p)

	req := httptest.NewRequest(http.MethodPost, "/paste", strings.NewReader("paste content"))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if receivedBody != "paste content" {
		t.Errorf("body received by backend after retry = %q, want %q", receivedBody, "paste content")
	}
}
