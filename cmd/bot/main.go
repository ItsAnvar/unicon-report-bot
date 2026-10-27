package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"unicon-report-bot/internal/bot"
	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/scheduler"
	"unicon-report-bot/internal/store"
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal("database connection failed; run go run ./cmd/diagnose -skip-telegram (connection details hidden)")
	}
	defer db.Pool.Close()
	if err := db.EnsureDeliveryQueue(ctx); err != nil {
		log.Fatal("delivery queue migration failed (details hidden)")
	}

	app, err := bot.New(cfg, db)
	if err != nil {
		log.Fatal("Telegram initialization failed; check BOT_TOKEN and network locally (connection details hidden)")
	}

	scheduler.Start(ctx, app.Bot, db, cfg)
	app.StartDeliveryWorker(ctx)
	log.Println("UNICON Daily Report Bot 2026-10-19.2 started; registration and persistent delivery enabled")
	go app.Start()
	<-ctx.Done()
	log.Println("Shutdown signal received")
	app.Bot.Stop()
}
