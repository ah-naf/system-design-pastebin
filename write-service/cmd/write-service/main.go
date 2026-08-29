package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/ah-naf/pastebin/shared/cache"
	"github.com/ah-naf/pastebin/shared/config"
	"github.com/ah-naf/pastebin/shared/id"
	"github.com/ah-naf/pastebin/shared/pgconn"
	"github.com/ah-naf/pastebin/write-service/internal/db"
	"github.com/ah-naf/pastebin/write-service/internal/handler"
	"github.com/ah-naf/pastebin/write-service/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalln(err)
	}

	conn, err := pgconn.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalln(err)
	}
	defer conn.Close()

	// Bound the pool: database/sql defaults to unlimited open connections,
	// which is fine for one replica but exhausts Postgres's max_connections
	// once you run several write-service pods against the same database.
	conn.SetMaxOpenConns(cfg.DBMaxOpenConns)
	conn.SetMaxIdleConns(cfg.DBMaxIdleConns)
	conn.SetConnMaxLifetime(cfg.DBConnMaxLifetime)

	if err = pgconn.RunMigrations(cfg.DatabaseURL, "infra/migrations"); err != nil {
		log.Fatalln(err)
	}

	redis := cache.NewClient(cfg.RedisAddr)
	defer redis.Close()
	idCounter := id.NewRedisCounterSource(redis)
	generator := id.NewGenerator(cfg.IDXORSecret, idCounter)

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 10*time.Second)
	s3Store, err := storage.NewStore(setupCtx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	cancelSetup()
	if err != nil {
		log.Fatalln(err)
	}

	repo := db.NewRepo(conn)

	h := handler.New(generator, s3Store, repo, cfg.PublicBaseURL, cfg.MaxPasteBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /paste", h.CreatePaste)
	mux.HandleFunc("GET /healthz", handler.Healthz(repo, s3Store))

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	// Listen for SIGINT/SIGTERM (Ctrl+C locally, what Kubernetes sends
	// before killing a pod) and shut the server down gracefully instead
	// of dropping in-flight requests.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
		stop()
		log.Println("shutting down write-service")

		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Fatalln("graceful shutdown failed:", err)
		}
	}
}
