package main

import (
	"context"
	"log"
	"net/http"
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

	if err = pgconn.RunMigrations(cfg.DatabaseURL, "infra/migrations"); err != nil {
		log.Fatalln(err)
	}

	redis := cache.NewClient(cfg.RedisAddr)
	idCounter := id.NewRedisCounterSource(redis)
	generator := id.NewGenerator(cfg.IDXORSecret, idCounter)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancel()
	s3Store, err := storage.NewStore(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)

	if err != nil {
		log.Fatalln(err)
	}

	repo := db.NewRepo(conn)

	h := handler.New(generator, s3Store, repo, cfg.PublicBaseURL, cfg.MaxPasteBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /paste", h.CreatePaste)
	mux.HandleFunc("GET /healthz", handler.Healthz(repo, s3Store))

	if err = http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		log.Fatalln(err)
	}

}
