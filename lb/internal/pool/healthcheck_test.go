package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartHealthChecksMarksBackendsAccordingly(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer unhealthy.Close()

	p := New([]string{healthy.URL, unhealthy.URL})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthChecks(ctx, 10*time.Millisecond, healthy.Client())

	time.Sleep(50 * time.Millisecond)

	if !p.backends[0].healthy.Load() {
		t.Error("healthy backend marked unhealthy after health checks ran")
	}
	if p.backends[1].healthy.Load() {
		t.Error("unhealthy backend (500 response) still marked healthy after health checks ran")
	}
}

func TestStartHealthChecksStopsOnContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New([]string{server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	p.StartHealthChecks(ctx, 10*time.Millisecond, server.Client())
	time.Sleep(30 * time.Millisecond) // let several checks run

	cancel()
	time.Sleep(30 * time.Millisecond) // let any in-flight check finish

	// Sentinel: the server keeps responding 200, so only a health check that
	// still runs after cancel would flip this back to true.
	p.backends[0].healthy.Store(false)

	time.Sleep(50 * time.Millisecond) // long enough for another tick if the loop didn't actually stop

	if p.backends[0].healthy.Load() {
		t.Error("a health check ran after context was canceled; StartHealthChecks should have stopped")
	}
}
