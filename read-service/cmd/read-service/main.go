package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	readCache "github.com/ah-naf/pastebin/read-service/internal/cache"
	"github.com/ah-naf/pastebin/read-service/internal/db"
	"github.com/ah-naf/pastebin/read-service/internal/handler"
	"github.com/ah-naf/pastebin/read-service/internal/storage"
	"github.com/ah-naf/pastebin/shared/cache"
	"github.com/ah-naf/pastebin/shared/config"
	"github.com/ah-naf/pastebin/shared/pgconn"
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

	conn.SetMaxOpenConns(cfg.DBMaxOpenConns)
	conn.SetMaxIdleConns(cfg.DBMaxIdleConns)
	conn.SetConnMaxLifetime(cfg.DBConnMaxLifetime)

	redisClient := cache.NewClient(cfg.RedisAddr)
	defer redisClient.Close()

	redis := readCache.NewCache(redisClient)

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	store, err := storage.NewStore(setupCtx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	cancelSetup()
	if err != nil {
		log.Fatalln(err)
	}

	repo := db.NewRepo(conn)

	h := handler.New(redis, redis, repo, store)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /paste/{id}", h.GetPaste)
	mux.HandleFunc("GET /healthz", handler.Healthz(repo, store))

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

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
		log.Println("shutting down read-service")

		shutDownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()

		if err := server.Shutdown(shutDownCtx); err != nil {
			log.Fatalln("graceful shutdown failed:", err)
		}
	}
}
