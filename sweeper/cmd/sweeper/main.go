package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/ah-naf/pastebin/shared/config"
	"github.com/ah-naf/pastebin/shared/pgconn"
	"github.com/ah-naf/pastebin/sweeper/internal/db"
	"github.com/ah-naf/pastebin/sweeper/internal/storage"
	"github.com/ah-naf/pastebin/sweeper/internal/sweep"
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

	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	store, err := storage.NewStore(cancelCtx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	cancel()
	if err != nil {
		log.Fatalln(err)
	}

	repo := db.NewRepo(conn)

	cancelCtx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	deleteCount, err := sweep.Run(cancelCtx, repo, store, cfg.SweeperBatchSize)
	cancel()
	log.Printf("swept %d expired pastes", deleteCount)
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
