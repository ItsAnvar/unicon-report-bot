package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/scheduler"
	"unicon-report-bot/internal/store"
)

// StartDeliveryWorker reconciles today's saved reports and retries each pending
// recipient separately. Successful jobs survive bot restarts in PostgreSQL.
func (a *App) StartDeliveryWorker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		var lastReconcile time.Time
		for {
			if ctx.Err() != nil {
				return
			}
			if time.Since(lastReconcile) >= 30*time.Second {
				check, cancel := context.WithTimeout(ctx, 10*time.Second)
				if err := a.DB.QueueReportsForDate(check, time.Now().In(a.Cfg.Timezone)); err != nil {
					log.Print("delivery reconciliation failed (details hidden)")
				}
				cancel()
				lastReconcile = time.Now()
			}
			if err := a.DeliverPending(ctx, 64); err != nil && ctx.Err() == nil {
				log.Print("delivery worker database error (details hidden)")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *App) DeliverPending(ctx context.Context, limit int) error {
	return a.DeliverPendingFor(ctx, 0, limit)
}

func (a *App) DeliverPendingFor(ctx context.Context, recipientID int64, limit int) error {
	for i := 0; i < limit && ctx.Err() == nil; i++ {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		job, err := a.DB.ClaimNotificationFor(check, recipientID)
		cancel()
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := a.deliverJob(ctx, job); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := a.notificationAction(ctx, func(c context.Context) error {
				return a.DB.RetryNotification(c, job, "delivery_internal_error", 30*time.Second)
			}); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (a *App) notificationAction(ctx context.Context, action func(context.Context) error) error {
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return action(check)
}

func deliveryAllowed(recipient store.Employee, job store.NotificationJob) bool {
	if !recipient.IsActive {
		return false
	}
	if recipient.Role == "advisor" {
		return true
	}
	return recipient.Role == "manager" && job.DepartmentID != 0 && recipient.DepartmentID == job.DepartmentID
}

func deliveryScope(recipient store.Employee) string {
	if recipient.Role == "advisor" {
		return "advisor:all"
	}
	return fmt.Sprintf("manager:%d", recipient.DepartmentID)
}

func (a *App) deliverJob(ctx context.Context, job store.NotificationJob) error {
	var recipient store.Employee
	if err := a.notificationAction(ctx, func(c context.Context) error {
		var err error
		recipient, err = a.DB.GetEmployee(c, job.RecipientID)
		return err
	}); err != nil {
		return err
	}
	if !deliveryAllowed(recipient, job) {
		return a.notificationAction(ctx, func(c context.Context) error { return a.DB.CancelNotification(c, job) })
	}
	renderScope := deliveryScope(recipient)
	if job.Message == "" || job.MessageScope != renderScope {
		job.NextPart = 0
		date, err := time.ParseInLocation("2006-01-02", job.Payload.Date, a.Cfg.Timezone)
		if err != nil {
			return err
		}
		scope := int64(0)
		if recipient.Role == "manager" {
			scope = recipient.DepartmentID
		}
		err = a.notificationAction(ctx, func(c context.Context) error {
			if job.Kind == "report" {
				done, missing, err := a.DB.StatusEmployeesByDepartment(c, date, scope)
				if err != nil {
					return err
				}
				at := job.Payload.At.In(a.Cfg.Timezone)
				job.Message = fmt.Sprintf("📨 YANGI HISOBOT\n📅 Sana: %s\n🕒 Vaqt: %s\n🏢 Bo‘lim: %s\n👤 Xodim: %s\n\n📝 Hisobot matni:\n%s\n\n%s", date.Format("02.01.2006"), at.Format("15:04"), job.Payload.DepartmentName, job.Payload.EmployeeName, job.Payload.Text, formatStatus(date, done, missing, recipient.DepartmentName, recipient.Role == "advisor"))
			} else {
				var err error
				job.Message, err = scheduler.BuildDailySummary(c, a.DB, date, scope, recipient.DepartmentName, recipient.Role == "advisor")
				if err != nil {
					return err
				}
			}
			return a.DB.SetNotificationMessage(c, job, job.Message, renderScope)
		})
		if err != nil {
			return err
		}
	}
	chunks := splitMessage(job.Message)
	for part := job.NextPart; part < len(chunks); part++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Check access again on retries and before every new part.
		if err := a.notificationAction(ctx, func(c context.Context) error {
			var err error
			recipient, err = a.DB.GetEmployee(c, job.RecipientID)
			return err
		}); err != nil {
			return err
		}
		if !deliveryAllowed(recipient, job) {
			return a.notificationAction(ctx, func(c context.Context) error { return a.DB.CancelNotification(c, job) })
		}
		if deliveryScope(recipient) != renderScope {
			return a.notificationAction(ctx, func(c context.Context) error { return a.DB.RetryNotification(c, job, "recipient_scope_changed", 0) })
		}
		if err := a.notificationAction(ctx, func(c context.Context) error { return a.DB.AdvanceNotification(c, job, part) }); err != nil {
			return err
		}
		if _, err := a.Bot.Send(&tele.User{ID: job.RecipientID}, chunks[part]); err != nil {
			code, delay := deliveryFailure(err, job.Attempts)
			log.Printf("notification %d for recipient %d deferred: %s", job.ID, job.RecipientID, code)
			return a.notificationAction(ctx, func(c context.Context) error { return a.DB.RetryNotification(c, job, code, delay) })
		}
		if err := a.notificationAction(ctx, func(c context.Context) error { return a.DB.AdvanceNotification(c, job, part+1) }); err != nil {
			return err
		}
	}
	return a.notificationAction(ctx, func(c context.Context) error { return a.DB.CompleteNotification(c, job) })
}

func deliveryFailure(err error, attempts int) (string, time.Duration) {
	var flood *tele.FloodError
	if errors.As(err, &flood) {
		delay := time.Duration(flood.RetryAfter) * time.Second
		if delay < 15*time.Second {
			delay = 15 * time.Second
		}
		return "rate_limited", delay
	}
	if errors.Is(err, tele.ErrBlockedByUser) {
		return "bot_blocked_by_user", time.Hour
	}
	if errors.Is(err, tele.ErrChatNotFound) {
		return "chat_not_started_or_not_found", time.Hour
	}
	var apiError *tele.Error
	if errors.As(err, &apiError) && apiError.Code == 403 {
		return "telegram_forbidden", time.Hour
	}
	if attempts > 6 {
		attempts = 6
	}
	return "network_or_telegram_error", 15 * time.Second * time.Duration(1<<attempts)
}

func (a *App) queueLeaderOverview(ctx context.Context, id int64) error {
	recipient, err := a.DB.GetEmployee(ctx, id)
	if err != nil {
		return err
	}
	date := time.Now().In(a.Cfg.Timezone)
	if err := a.DB.QueueLeaderSummary(ctx, recipient, date, "role_summary"); err != nil {
		return err
	}
	return a.DB.QueueReportsForDate(ctx, date)
}

func (a *App) deliveries(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Send("Bu buyruq faqat administrator uchun.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	entries, err := a.DB.DeliveryStatuses(ctx)
	if err != nil {
		return c.Send("Yetkazish navbatini olishda xatolik.")
	}
	var message strings.Builder
	message.WriteString("📬 AVTOMATIK YETKAZISH HOLATI\n")
	for _, entry := range entries {
		fmt.Fprintf(&message, "\n%s — %s (%s)\nYetkazildi: %d; navbatda: %d; xatoli: %d\n", entry.Name, roleName(entry.Role), entry.Department, entry.Delivered, entry.Pending, entry.Failed)
		if entry.LastError != "" {
			fmt.Fprintf(&message, "Sabab: %s\n", entry.LastError)
		}
		if entry.Role == "manager" && entry.Department == "" {
			message.WriteString("⚠️ Bo‘lim biriktirilmagan: /setdept kerak.\n")
		}
	}
	message.WriteString("\nBot bloklangan yoki suhbat topilmagan bo‘lsa, foydalanuvchi botni ochib /start yuborsin. Qayta urinish: /retrydeliveries")
	return sendLong(c, message.String())
}

func (a *App) retryDeliveries(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Send("Bu buyruq faqat administrator uchun.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := a.DB.QueueReportsForDate(ctx, time.Now().In(a.Cfg.Timezone)); err != nil {
		return c.Send("Navbatni yangilashda xatolik.")
	}
	if err := a.DB.WakeNotifications(ctx, 0); err != nil {
		return c.Send("Qayta yuborishni rejalashda xatolik.")
	}
	return c.Send("Yetkazilmagan xabarlar qayta urinishga qo‘yildi. Natija: /deliveries")
}
