package bot

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/config"
)

func TestConcurrentClaimsAndExpiredLease(t *testing.T) {
	a, _, _ := deliveryFixture(t)
	ctx := context.Background()
	if err := a.DB.SaveReport(ctx, 101, time.Now().In(a.Cfg.Timezone), "Lease test"); err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		jobID, lease int64
		err          error
	}
	results := make(chan claimResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			job, err := a.DB.ClaimNotificationFor(ctx, 103)
			results <- claimResult{job.ID, job.Lease, err}
		}()
	}
	var claimed claimResult
	successes := 0
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err == nil {
			claimed = result
			successes++
		} else if !errors.Is(result.err, pgx.ErrNoRows) {
			t.Fatal(result.err)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent workers claimed the same job")
	}
	if _, err := a.DB.Pool.Exec(ctx, `UPDATE notification_jobs SET locked_until=NOW()-INTERVAL '1 second' WHERE id=$1`, claimed.jobID); err != nil {
		t.Fatal(err)
	}
	newLease, err := a.DB.ClaimNotificationFor(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	if newLease.ID != claimed.jobID || newLease.Lease <= claimed.lease {
		t.Fatal("expired job was not safely reclaimed")
	}
	oldLease := newLease
	oldLease.Lease = claimed.lease
	if err := a.DB.AdvanceNotification(ctx, oldLease, 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("stale worker advanced a reclaimed job")
	}
}

func deliveryFixture(t *testing.T) (*App, *memoryTelegram, int64) {
	t.Helper()
	db := workflowDB(t)
	ctx := context.Background()
	departments, err := db.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, d := range departments {
		ids[d.Name] = d.ID
	}
	for _, e := range []struct {
		id               int64
		name, role, dept string
	}{
		{101, "IUTXR employee", "employee", "IUTXRvaEQB"},
		{102, "EQB employee", "employee", "Boshqa bo‘lim"},
		{103, "Manager", "manager", "IUTXRvaEQB"},
		{104, "Other manager", "manager", "Boshqa bo‘lim"},
		{105, "Advisor 1", "advisor", "IUTXRvaEQB"},
		{106, "Advisor 2", "advisor", "IUTXRvaEQB"},
		{107, "Advisor 3", "advisor", "IUTXRvaEQB"},
	} {
		if err := db.Approve(ctx, e.id, e.name, e.role, 999); err != nil {
			t.Fatal(err)
		}
		if err := db.AssignDepartment(ctx, e.id, ids[e.dept]); err != nil {
			t.Fatal(err)
		}
	}
	transport := &memoryTelegram{}
	b, err := tele.NewBot(tele.Settings{Offline: true, Token: "test", URL: "https://offline.invalid", Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	return &App{Bot: b, DB: db, Cfg: config.Config{Timezone: time.FixedZone("Asia/Tashkent", 5*60*60), AdminIDs: map[int64]bool{}}}, transport, ids["IUTXRvaEQB"]
}

func recipientMessages(m *memoryTelegram, id string) []string {
	var result []string
	for _, msg := range m.messages {
		if msg.ChatID == id {
			result = append(result, msg.Text)
		}
	}
	return result
}

func TestThreeAdvisorsAndManagerRetryIndependently(t *testing.T) {
	a, transport, _ := deliveryFixture(t)
	ctx := context.Background()
	now := time.Now().In(a.Cfg.Timezone)
	transport.failOnAttempt = map[string]int{"107": 1, "103": 1}
	if err := a.DB.SaveReport(ctx, 101, now, "Automatic report"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"105", "106"} {
		if len(recipientMessages(transport, id)) != 1 {
			t.Fatal("successful advisor did not receive report")
		}
	}
	if len(recipientMessages(transport, "103")) != 0 || len(recipientMessages(transport, "107")) != 0 {
		t.Fatal("failure was not simulated")
	}
	if len(recipientMessages(transport, "104")) != 0 {
		t.Fatal("report leaked to another department")
	}
	entries, err := a.DB.DeliveryStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, entry := range entries {
		failed += entry.Failed
	}
	if failed != 2 {
		t.Fatal("failed recipients were not recorded")
	}
	if err := a.DB.WakeNotifications(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// Recreate the application to demonstrate a restart does not lose pending jobs.
	restarted := &App{Bot: a.Bot, DB: a.DB, Cfg: a.Cfg}
	if err := restarted.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"103", "105", "106", "107"} {
		if len(recipientMessages(transport, id)) != 1 {
			t.Fatalf("recipient %s missing or duplicated", id)
		}
	}
	if err := a.DB.QueueReportsForDate(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"103", "105", "106", "107"} {
		if len(recipientMessages(transport, id)) != 1 {
			t.Fatal("startup reconciliation duplicated a successful delivery")
		}
	}
	// A newly approved advisor receives reports saved before their approval.
	if err := a.DB.Approve(ctx, 108, "Late advisor", "advisor", 999); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.QueueReportsForDate(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	if len(recipientMessages(transport, "108")) != 1 {
		t.Fatal("new advisor did not receive existing report")
	}
}

func TestRetryResumesAfterSuccessfulChunk(t *testing.T) {
	a, transport, _ := deliveryFixture(t)
	ctx := context.Background()
	now := time.Now().In(a.Cfg.Timezone)
	transport.failOnAttempt = map[string]int{"107": 2}
	if err := a.DB.SaveReport(ctx, 101, now, strings.Repeat("Uzbek📝", 800)); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	first := recipientMessages(transport, "107")
	if len(first) != 1 {
		t.Fatal("expected one successful chunk before failure")
	}
	if err := a.DB.WakeNotifications(ctx, 107); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	parts := recipientMessages(transport, "107")
	if len(parts) < 2 {
		t.Fatal("remaining chunks were not delivered")
	}
	if parts[1] == first[0] {
		t.Fatal("retry resent the acknowledged first chunk")
	}
	var saved string
	if err := a.DB.Pool.QueryRow(ctx, `SELECT message FROM notification_jobs WHERE recipient_telegram_id=107`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if strings.Join(parts, "") != saved {
		t.Fatal("chunk retry lost or duplicated text")
	}
}

func TestRetryRechecksRoleAndRenderedScope(t *testing.T) {
	a, transport, iutxr := deliveryFixture(t)
	ctx := context.Background()
	now := time.Now().In(a.Cfg.Timezone)
	transport.failOnAttempt = map[string]int{"107": 1, "106": 1}
	if err := a.DB.SaveReport(ctx, 101, now, "Private report"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Approve(ctx, 107, "Changed manager", "manager", 999); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.AssignDepartment(ctx, 107, iutxr); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Pool.Exec(ctx, `UPDATE employees SET is_active=FALSE WHERE telegram_id=106`); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.WakeNotifications(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(recipientMessages(transport, "107"), "")
	if !strings.Contains(text, "Private report") || strings.Contains(text, "EQB employee") {
		t.Fatal("retry used the old advisor-wide message after role change")
	}
	if len(recipientMessages(transport, "106")) != 0 {
		t.Fatal("deactivated recipient received a queued report")
	}
}

func TestReportAndRecipientQueueAreAtomic(t *testing.T) {
	a, _, _ := deliveryFixture(t)
	ctx := context.Background()
	if _, err := a.DB.Pool.Exec(ctx, `ALTER TABLE notification_jobs RENAME TO unavailable_jobs`); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.SaveReport(ctx, 101, time.Now(), "Must roll back"); err == nil {
		t.Fatal("missing queue should fail the transaction")
	}
	var count int
	if err := a.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM reports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("report was committed without its notification jobs")
	}
}

func TestDeliverPendingForOnlyRequestedRecipient(t *testing.T) {
	a, transport, _ := deliveryFixture(t)
	ctx := context.Background()
	if err := a.DB.SaveReport(ctx, 101, time.Now().In(a.Cfg.Timezone), "Selected recipient test"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPendingFor(ctx, 103, 100); err != nil {
		t.Fatal(err)
	}
	if len(transport.messages) != 1 || transport.messages[0].ChatID != "103" {
		t.Fatal("selected delivery touched another recipient")
	}
}

func TestDailySummaryQueueIsPersistentAndIdempotent(t *testing.T) {
	a, transport, _ := deliveryFixture(t)
	ctx := context.Background()
	now := time.Now().In(a.Cfg.Timezone)
	recipients, err := a.DB.ReportRecipients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		for _, recipient := range recipients {
			if err := a.DB.QueueLeaderSummary(ctx, recipient, now, "daily_summary"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range recipients {
		if err := a.DB.QueueLeaderSummary(ctx, recipient, now, "daily_summary"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"103", "104", "105", "106", "107"} {
		if len(recipientMessages(transport, id)) != 1 {
			t.Fatal("daily summary duplicated or missing")
		}
	}
}
