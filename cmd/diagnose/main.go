// diagnose checks connections without polling Telegram or sending messages.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/store"
)

func main() {
	skipTelegram := flag.Bool("skip-telegram", false, "Check local configuration and PostgreSQL without accessing Telegram")
	flag.Parse()
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("FAIL: configuration invalid; check required values, IDs, timezone and times in .env")
		os.Exit(1)
	}
	fmt.Println("OK: configuration loaded (secrets hidden)")
	failed := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Println("FAIL: PostgreSQL connection; check service and DATABASE_URL locally")
		failed = true
	} else {
		defer db.Pool.Close()
		fmt.Println("OK: PostgreSQL connection")
		// Parse all columns used by the bot without reading employee/report data.
		_, err = db.Pool.Exec(ctx, `SELECT e.id,e.telegram_id,e.full_name,e.role,e.is_active,e.department_id,e.updated_at,
		 d.id,d.name,r.employee_id,r.report_date,r.report_text,r.submitted_at,r.updated_at,r.department_id,
		 a.telegram_id,a.telegram_username,a.requested_name,a.status,a.requested_at,a.reviewed_at,a.reviewed_by,a.department_id
		 FROM employees e,departments d,reports r,access_requests a LIMIT 0`)
		if err != nil {
			fmt.Println("FAIL: database schema; apply migrations 001, 002 and 003 in order")
			failed = true
		} else {
			fmt.Println("OK: database schema")
			if _, err := db.Pool.Exec(ctx, `SELECT telegram_id,department_id,step,full_name FROM registration_drafts LIMIT 0`); err != nil {
				fmt.Println("FAIL: registration schema; apply migration 006")
				failed = true
			} else {
				fmt.Println("OK: persistent registration steps")
			}
			_, err = db.Pool.Exec(ctx, `SELECT d.is_active,a.requested_role,m.action,m.token,m.expected_version,u.actor_id
 FROM departments d,access_requests a,admin_drafts m,management_audit u LIMIT 0`)
			if err != nil {
				fmt.Println("FAIL: role and department management schema; apply migration 006")
				failed = true
			} else {
				fmt.Println("OK: role and department management schema")
			}
			now := time.Now().In(cfg.Timezone)
			submitted, missing, statusErr := db.StatusEmployeesByDepartment(ctx, now, 0)
			if statusErr != nil {
				fmt.Println("FAIL: current status query")
				failed = true
			} else {
				fmt.Printf("OK: local date %s (%s); submitted=%d missing=%d\n", now.Format("2006-01-02"), cfg.Timezone, len(submitted), len(missing))
			}
			entries, err := db.DeliveryStatuses(ctx)
			if err != nil {
				fmt.Println("FAIL: delivery queue schema; apply migration 004")
				failed = true
			} else {
				fmt.Printf("OK: delivery queue; active leaders=%d\n", len(entries))
				for _, entry := range entries {
					fmt.Printf("%s (%s, %s): delivered=%d pending=%d failed=%d error=%s\n", entry.Name, entry.Role, entry.Department, entry.Delivered, entry.Pending, entry.Failed, entry.LastError)
				}
			}
		}
	}
	if *skipTelegram {
		fmt.Println("SKIP: Telegram access disabled")
	} else {
		_, err = tele.NewBot(tele.Settings{Token: cfg.BotToken, Client: &http.Client{Timeout: 10 * time.Second}})
		if err != nil {
			fmt.Println("FAIL: Telegram getMe; check token and network locally")
			failed = true
		} else {
			fmt.Println("OK: Telegram authentication (no messages sent)")
		}
	}
	if failed {
		os.Exit(1)
	}
}
