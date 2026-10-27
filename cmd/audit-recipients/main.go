// Read Telegram chat metadata for known bot users; never send messages.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/bot"
	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/store"
)

func main() {
	username := flag.String("find-username", "", "Resolve a username only among known bot user IDs")
	repair := flag.Bool("make-advisor", false, "Assign advisor only to the exact live username supplied with -find-username")
	deliver := flag.Bool("deliver-pending", false, "Send queued reports only to the exact matched username; requires explicit user authorization")
	flag.Parse()
	if (*repair || *deliver) && strings.TrimSpace(*username) == "" {
		fmt.Println("FAIL: provide exact username")
		os.Exit(1)
	}
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("FAIL: local configuration (details hidden)")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Println("FAIL: database connection (details hidden)")
		os.Exit(1)
	}
	defer db.Pool.Close()
	if err := db.EnsureDeliveryQueue(ctx); err != nil {
		fmt.Println("FAIL: delivery schema (details hidden)")
		os.Exit(1)
	}
	b, err := tele.NewBot(tele.Settings{Token: cfg.BotToken, Client: &http.Client{Timeout: 10 * time.Second}})
	if err != nil {
		fmt.Println("FAIL: Telegram authentication (details hidden)")
		os.Exit(1)
	}
	fmt.Printf("Bot: @%s; no messages sent\n", b.Me.Username)
	users, err := db.ActiveEmployees(ctx)
	if err != nil {
		fmt.Println("FAIL: load known users")
		os.Exit(1)
	}
	known := map[int64]bool{}
	for _, user := range users {
		known[user.TelegramID] = true
	}
	for id := range cfg.AdminIDs {
		known[id] = true
	}
	pending, err := db.PendingRequests(ctx)
	if err != nil {
		fmt.Println("FAIL: load pending requests")
		os.Exit(1)
	}
	for _, request := range pending {
		known[request.TelegramID] = true
	}
	target := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(*username), "@"))
	var matches []*tele.Chat
	for id := range known {
		user, userErr := db.GetEmployee(ctx, id)
		chat, chatErr := b.ChatByID(id)
		isLeader := userErr == nil && (user.Role == "manager" || user.Role == "advisor")
		if chatErr != nil {
			if isLeader || cfg.AdminIDs[id] {
				fmt.Printf("%s role=%s department=%s chat_metadata=UNAVAILABLE\n", user.FullName, user.Role, user.DepartmentName)
			}
			continue
		}
		if err := db.RememberUsername(ctx, id, chat.Username); err != nil {
			fmt.Println("WARN: username synchronization failed")
		}
		matched := target != "" && strings.EqualFold(chat.Username, target)
		if matched {
			matches = append(matches, chat)
		}
		if isLeader || matched || cfg.AdminIDs[id] {
			role := user.Role
			if userErr != nil {
				role = "NOT_REGISTERED"
			}
			fmt.Printf("@%s (%s %s) role=%s department=%s chat_metadata=OK configured_admin=%t\n", chat.Username, chat.FirstName, chat.LastName, role, user.DepartmentName, cfg.AdminIDs[id])
		}
	}
	if target != "" && len(matches) == 0 {
		fmt.Printf("NOT FOUND: @%s among known user IDs; this person must /start @%s and share /whoami ID\n", target, b.Me.Username)
	}
	if *repair {
		if len(matches) != 1 {
			fmt.Println("NO CHANGE: exact unique account not resolved")
			os.Exit(1)
		}
		chat := matches[0]
		name := strings.TrimSpace(chat.FirstName + " " + chat.LastName)
		if name == "" {
			name = chat.Username
		}
		if err := db.Approve(ctx, chat.ID, name, "advisor", 0); err != nil {
			fmt.Println("FAIL: advisor role update")
			os.Exit(1)
		}
		if err := db.RememberUsername(ctx, chat.ID, chat.Username); err != nil {
			fmt.Println("FAIL: username update")
			os.Exit(1)
		}
		user, err := db.GetEmployee(ctx, chat.ID)
		if err != nil {
			fmt.Println("FAIL: verify advisor")
			os.Exit(1)
		}
		now := time.Now().In(cfg.Timezone)
		if err := db.QueueLeaderSummary(ctx, user, now, "role_summary"); err != nil {
			fmt.Println("FAIL: queue advisor overview")
			os.Exit(1)
		}
		if err := db.QueueReportsForDate(ctx, now); err != nil {
			fmt.Println("FAIL: queue today's reports")
			os.Exit(1)
		}
		fmt.Printf("UPDATED: @%s advisor; today's reports queued; no messages sent by audit\n", chat.Username)
	}
	if *deliver {
		if len(matches) != 1 {
			fmt.Println("NO DELIVERY: exact unique account not resolved")
			os.Exit(1)
		}
		chat := matches[0]
		user, err := db.GetEmployee(ctx, chat.ID)
		if err != nil || !user.IsActive || (user.Role != "advisor" && user.Role != "manager") {
			fmt.Println("NO DELIVERY: account is not an active leader")
			os.Exit(1)
		}
		if err := db.QueueReportsForDate(ctx, time.Now().In(cfg.Timezone)); err != nil {
			fmt.Println("FAIL: queue saved reports")
			os.Exit(1)
		}
		if err := db.WakeNotifications(ctx, chat.ID); err != nil {
			fmt.Println("FAIL: wake selected recipient")
			os.Exit(1)
		}
		app := &bot.App{Bot: b, DB: db, Cfg: cfg}
		if err := app.DeliverPendingFor(ctx, chat.ID, 100); err != nil {
			fmt.Println("FAIL: delivery database operation (details hidden)")
			os.Exit(1)
		}
		entries, err := db.DeliveryStatuses(ctx)
		if err != nil {
			fmt.Println("FAIL: delivery status")
			os.Exit(1)
		}
		for _, entry := range entries {
			if entry.Name == user.FullName && entry.Role == user.Role {
				fmt.Printf("DELIVERY: @%s delivered=%d pending=%d failed=%d code=%s\n", chat.Username, entry.Delivered, entry.Pending, entry.Failed, entry.LastError)
			}
		}
	}
}
