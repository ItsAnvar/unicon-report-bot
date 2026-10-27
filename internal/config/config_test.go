package config

import "testing"

func validEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"REPORT_GROUP_CHAT_ID", "ADMIN_TELEGRAM_IDS", "TIMEZONE", "REPORT_DEADLINE", "REMINDER_TIME", "DAILY_SUMMARY_TIME"} {
		t.Setenv(key, "")
	}
	t.Setenv("BOT_TOKEN", "local-test-token")
	t.Setenv("DATABASE_URL", "postgres://test@localhost/test")
}

func TestDefaultScheduleAndTimezone(t *testing.T) {
	validEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DailySummaryTime != "19:00" || cfg.Timezone.String() != "Asia/Tashkent" {
		t.Fatal("wrong default reporting schedule")
	}
}

func TestRejectInvalidSettings(t *testing.T) {
	for _, entry := range []struct{ key, value string }{
		{"BOT_TOKEN", ""}, {"DATABASE_URL", ""}, {"ADMIN_TELEGRAM_IDS", "-1"}, {"ADMIN_TELEGRAM_IDS", "0"},
		{"TIMEZONE", "Not/AZone"}, {"DAILY_SUMMARY_TIME", "9:00"}, {"DAILY_SUMMARY_TIME", "24:00"}, {"REMINDER_TIME", "17:99"}, {"REPORT_DEADLINE", "wrong"},
	} {
		t.Run(entry.key+entry.value, func(t *testing.T) {
			validEnvironment(t)
			t.Setenv(entry.key, entry.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
