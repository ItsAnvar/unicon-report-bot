package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken         string
	DatabaseURL      string
	ReportGroupID    int64
	AdminIDs         map[int64]bool
	Timezone         *time.Location
	ReportDeadline   string
	ReminderTime     string
	DailySummaryTime string
}

func Load() (Config, error) {
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		return Config{}, fmt.Errorf("BOT_TOKEN is required")
	}
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	cfg := Config{
		BotToken:         token,
		DatabaseURL:      dbURL,
		AdminIDs:         map[int64]bool{},
		ReportDeadline:   envOr("REPORT_DEADLINE", "17:30"),
		ReminderTime:     envOr("REMINDER_TIME", "17:00"),
		DailySummaryTime: envOr("DAILY_SUMMARY_TIME", "19:00"),
	}
	if raw := strings.TrimSpace(os.Getenv("REPORT_GROUP_CHAT_ID")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("invalid REPORT_GROUP_CHAT_ID: %w", err)
		}
		cfg.ReportGroupID = id
	}
	for _, raw := range strings.Split(os.Getenv("ADMIN_TELEGRAM_IDS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return Config{}, fmt.Errorf("admin Telegram IDs must be positive integers")
		}
		cfg.AdminIDs[id] = true
	}
	loc, err := time.LoadLocation(envOr("TIMEZONE", "Asia/Tashkent"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid TIMEZONE: %w", err)
	}
	cfg.Timezone = loc
	if _, err := time.Parse("15:04", cfg.ReportDeadline); err != nil || len(cfg.ReportDeadline) != 5 {
		return Config{}, fmt.Errorf("REPORT_DEADLINE must be HH:MM")
	}
	if _, err := time.Parse("15:04", cfg.ReminderTime); err != nil || len(cfg.ReminderTime) != 5 {
		return Config{}, fmt.Errorf("REMINDER_TIME must be HH:MM")
	}
	if _, err := time.Parse("15:04", cfg.DailySummaryTime); err != nil || len(cfg.DailySummaryTime) != 5 {
		return Config{}, fmt.Errorf("DAILY_SUMMARY_TIME must be HH:MM")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
