// migrate applies reviewed, embedded upgrades without connecting to Telegram.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"unicon-report-bot/internal/store"
)

func main() {
	_ = godotenv.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := store.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Println("FAIL: PostgreSQL connection (details hidden)")
		os.Exit(1)
	}
	defer db.Pool.Close()
	if err := db.EnsureDeliveryQueue(ctx); err != nil {
		fmt.Println("FAIL: migrations rolled back (details hidden); keep the backup and check the local database schema")
		os.Exit(1)
	}
	fmt.Println("OK: migrations 004, 005, 006, 007 recorded; existing records preserved; Telegram not contacted")
}
