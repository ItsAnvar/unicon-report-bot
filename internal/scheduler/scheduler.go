package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/store"

	tele "gopkg.in/telebot.v3"
)

func Start(ctx context.Context, b *tele.Bot, db *store.Store, cfg config.Config) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		seen := map[string]bool{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().In(cfg.Timezone)
				dateKey := now.Format("2006-01-02")
				minuteKey := now.Format("15:04")

				// Durable keys make repeated scans and restarts safe, and let newly
				// approved leaders join today's summary after the scheduled time.
				if now.Format("15:04") >= cfg.DailySummaryTime {
					if err := QueueDailySummaries(ctx, db, cfg, now); err != nil {
						log.Print("queue daily summaries failed (details hidden)")
					}
				}
				// Reminders/deadline notices are workday-only.
				if now.Weekday() != time.Saturday && now.Weekday() != time.Sunday {
					if now.Format("15:04") == cfg.ReminderTime && !seen[dateKey+":reminder:"+minuteKey] {
						sendMissing(ctx, b, db, cfg, now, false)
						seen[dateKey+":reminder:"+minuteKey] = true
					}
					if now.Format("15:04") == cfg.ReportDeadline && !seen[dateKey+":deadline:"+minuteKey] {
						sendMissing(ctx, b, db, cfg, now, true)
						seen[dateKey+":deadline:"+minuteKey] = true
					}
				}

				for key := range seen {
					if len(key) < 10 || key[:10] != dateKey {
						delete(seen, key)
					}
				}
			}
		}
	}()
}

func QueueDailySummaries(ctx context.Context, db *store.Store, cfg config.Config, now time.Time) error {
	now = now.In(cfg.Timezone)
	if now.Format("15:04") < cfg.DailySummaryTime {
		return nil
	}
	recipients, err := db.ReportRecipients(ctx)
	if err != nil {
		return err
	}
	var failed bool
	for _, recipient := range recipients {
		if err := db.QueueLeaderSummary(ctx, recipient, now, "daily_summary"); err != nil {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("some leaders have no department or could not be queued")
	}
	return nil
}

// BuildDailySummary is shared by scheduled delivery and the leaders' /start view.
func BuildDailySummary(ctx context.Context, db *store.Store, now time.Time, departmentID int64, departmentName string, allDepartments bool) (string, error) {
	submitted, missing, err := db.StatusEmployeesByDepartment(ctx, now, departmentID)
	if err != nil {
		return "", fmt.Errorf("load submission status: %w", err)
	}
	reports, err := db.ReportsForDateByDepartment(ctx, now, departmentID)
	if err != nil {
		return "", fmt.Errorf("load reports: %w", err)
	}

	var out strings.Builder
	out.WriteString("📊 KUNLIK HISOBOTLAR\n")
	out.WriteString("📅 Sana: " + now.Format("02.01.2006") + "\n")
	if allDepartments {
		out.WriteString("🏢 Qamrov: barcha bo‘limlar\n")
	} else if departmentName != "" {
		out.WriteString("🏢 Bo‘lim: " + departmentName + "\n")
	} else if len(submitted) > 0 && submitted[0].DepartmentName != "" {
		out.WriteString("🏢 Bo‘lim: " + submitted[0].DepartmentName + "\n")
	} else if len(missing) > 0 && missing[0].DepartmentName != "" {
		out.WriteString("🏢 Bo‘lim: " + missing[0].DepartmentName + "\n")
	}
	out.WriteString(fmt.Sprintf("\n✅ Hisobot topshirganlar: %d\n⏳ Topshirmaganlar: %d\n\n", len(submitted), len(missing)))
	if len(submitted) > 0 {
		out.WriteString("✅ TOPSHIRGANLAR\n")
		for _, e := range submitted {
			if allDepartments && e.DepartmentName != "" {
				out.WriteString(fmt.Sprintf("• %s — %s\n", e.FullName, e.DepartmentName))
			} else {
				out.WriteString("• " + e.FullName + "\n")
			}
		}
	} else {
		out.WriteString("✅ TOPSHIRGANLAR\nBugun hali hech kim hisobot topshirmagan.\n")
	}
	if len(missing) > 0 {
		out.WriteString("\n⏳ TOPSHIRMAGANLAR\n")
		for _, e := range missing {
			if allDepartments && e.DepartmentName != "" {
				out.WriteString(fmt.Sprintf("• %s — %s\n", e.FullName, e.DepartmentName))
			} else {
				out.WriteString("• " + e.FullName + "\n")
			}
		}
	} else {
		out.WriteString("\n⏳ TOPSHIRMAGANLAR\nYo‘q.\n")
	}
	if len(reports) > 0 {
		out.WriteString("\n📝 HISOBOT MATNLARI\n\n")
		for i, r := range reports {
			if allDepartments && r.DepartmentName != "" {
				out.WriteString(fmt.Sprintf("%d. %s (%s)\n", i+1, r.FullName, r.DepartmentName))
			} else {
				out.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.FullName))
			}
			out.WriteString(r.ReportText + "\n\n")
		}
	}
	return out.String(), nil
}

func sendMissing(ctx context.Context, b *tele.Bot, db *store.Store, cfg config.Config, now time.Time, deadline bool) {
	submitted, missingIDs, err := db.StatusForDate(ctx, now)
	if err != nil {
		log.Printf("scheduler status: %v", err)
		return
	}
	if len(missingIDs) == 0 {
		return
	}
	employees, err := db.ActiveEmployees(ctx)
	if err != nil {
		log.Printf("scheduler employees: %v", err)
		return
	}
	missing := map[int64]bool{}
	for _, id := range missingIDs {
		missing[id] = true
	}
	if deadline {
		for _, e := range employees {
			if e.Role == "employee" && missing[e.TelegramID] {
				_, _ = b.Send(&tele.User{ID: e.TelegramID}, "⏰ Bugungi hisobotni hali topshirmadingiz. Iltimos, /report orqali yuboring.")
			}
		}
	} else {
		for _, e := range employees {
			if e.Role == "employee" && missing[e.TelegramID] {
				_, _ = b.Send(&tele.User{ID: e.TelegramID}, "🔔 Eslatma: bugungi ish hisobotingizni kun oxirigacha yuboring. /report")
			}
		}
	}
	if cfg.ReportGroupID != 0 {
		title := "🔔 HISOBOT ESLATMASI"
		if deadline {
			title = "⏰ HISOBOT MUDDATI YAKUNLANDI"
		}
		msg := fmt.Sprintf("%s\nSana: %s\nTopshirganlar: %d\nTopshirmaganlar: %d", title, now.Format("02.01.2006"), len(submitted), len(missingIDs))
		if _, err := b.Send(&tele.Chat{ID: cfg.ReportGroupID}, msg); err != nil {
			log.Print("scheduler group message failed (details hidden)")
		}
	}
}
