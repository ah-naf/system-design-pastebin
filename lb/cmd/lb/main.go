package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/ah-naf/pastebin/lb/internal/config"
	"github.com/ah-naf/pastebin/lb/internal/pool"
	"github.com/ah-naf/pastebin/lb/internal/proxy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalln(err)
	}

	writePool := pool.New(cfg.WriteBackends)
	readPool := pool.New(cfg.ReadBackends)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	healthCheckClient := &http.Client{
		Timeout: 3 * time.Second,
	}
	writePool.StartHealthChecks(ctx, cfg.HealthCheckInterval, healthCheckClient)
	readPool.StartHealthChecks(ctx, cfg.HealthCheckInterval, healthCheckClient)

	mux := http.NewServeMux()
	mux.Handle("POST /paste", proxy.New(writePool))
	mux.Handle("GET /paste/{id}", proxy.New(readPool))
	mux.HandleFunc("GET /healthz", healthz(writePool, readPool))

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalln(err)
		}
	case <-ctx.Done():
		log.Println("shutting down lb")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Fatalln("graceful shutdown failed:", err)
		}
	}
}

func healthz(writePool, readPool *pool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !writePool.HasHealthy() {
			http.Error(w, "no healthy write backend", http.StatusServiceUnavailable)
			return
		}
		if !readPool.HasHealthy() {
			http.Error(w, "no healthy read backend", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}
}
