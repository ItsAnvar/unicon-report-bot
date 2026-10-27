package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/scheduler"
)

func TestRegistrationApprovalAndDirectorDelivery(t *testing.T) {
	db := workflowDB(t)
	ctx := context.Background()
	transport := &memoryTelegram{}
	b, err := tele.NewBot(tele.Settings{Offline: true, Token: "test", URL: "https://offline.invalid", Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AdminIDs: map[int64]bool{999: true}, Timezone: time.FixedZone("Asia/Tashkent", 5*60*60), DailySummaryTime: "19:00"}
	a := &App{Bot: b, DB: db, Cfg: cfg}
	userContext := func(id int64, text string) tele.Context {
		return b.NewContext(tele.Update{Message: &tele.Message{Sender: &tele.User{ID: id, Username: "test_user", FirstName: "Untrusted Profile Name"}, Chat: &tele.Chat{ID: id, Type: tele.ChatPrivate}, Text: text}})
	}
	if err := a.start(userContext(201, "/start")); err != nil {
		t.Fatal(err)
	}
	if len(transport.messages) != 1 || !strings.Contains(transport.messages[0].Text, "Ism va familiya") {
		t.Fatal("start did not ask for full name")
	}
	var requestCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM access_requests WHERE telegram_id=201`).Scan(&requestCount); err != nil || requestCount != 0 {
		t.Fatal("admin request was created before name entry")
	}
	if err := a.onText(userContext(201, "Onlyname")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAccessRequest(ctx, 201); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("invalid name reached approval")
	}
	if err := a.onText(userContext(201, "O‘ktam Rahimov")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(transport.messages[len(transport.messages)-1].ReplyMarkup), "🏢 Bosh direktor maslahatchisi") {
		t.Fatal("director choice missing")
	}
	if err := a.onText(userContext(201, "IUTXRvaEQB")); err != nil {
		t.Fatal(err)
	}
	draft, err := db.RegistrationDraft(ctx, 201)
	if err != nil || draft.Step != "role" || draft.DepartmentName != "IUTXRvaEQB" {
		t.Fatal("department choice was not persisted")
	}
	// A new process must resume the role step from PostgreSQL.
	a = &App{Bot: b, DB: db, Cfg: cfg}
	if err := a.start(userContext(201, "/start")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transport.messages[len(transport.messages)-1].Text, "Rolingizni") {
		t.Fatal("registration did not resume after restart")
	}
	if err := a.onText(userContext(201, "Onlyname")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAccessRequest(ctx, 201); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("incomplete full name reached approval")
	}
	if err := a.onText(userContext(201, "👤 Xodim")); err != nil {
		t.Fatal(err)
	}
	req, err := db.GetAccessRequest(ctx, 201)
	if err != nil {
		t.Fatal(err)
	}
	if req.RequestedName != "O‘ktam Rahimov" || req.DepartmentName != "IUTXRvaEQB" {
		t.Fatal("request lost entered name or selected department")
	}
	adminMessages := recipientMessages(transport, "999")
	if len(adminMessages) != 1 || !strings.Contains(adminMessages[0], "O‘ktam Rahimov") || !strings.Contains(adminMessages[0], "IUTXRvaEQB") {
		t.Fatal("admin did not receive complete request")
	}
	if _, err := db.GetEmployee(ctx, 201); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("user activated before admin approval")
	}
	if err := a.start(userContext(201, "/start")); err != nil {
		t.Fatal(err)
	}
	if len(recipientMessages(transport, "999")) != 1 {
		t.Fatal("start duplicated a pending approval notification")
	}
	callback := func(admin int64, role string) tele.Context {
		return b.NewContext(tele.Update{Callback: &tele.Callback{ID: "test-callback", Sender: &tele.User{ID: admin}, Message: &tele.Message{ID: 1, Chat: &tele.Chat{ID: admin, Type: tele.ChatPrivate}}, Data: fmt.Sprintf("201:%s:%d", role, req.RequestedAt.UnixMicro())}})
	}
	if err := a.assignRoleCallback(callback(888, "employee")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetEmployee(ctx, 201); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("non-admin approved a user")
	}
	if err := a.assignRoleCallback(callback(999, "employee")); err != nil {
		t.Fatal(err)
	}
	employee, err := db.GetEmployee(ctx, 201)
	if err != nil || !employee.IsActive || employee.FullName != "O‘ktam Rahimov" || employee.DepartmentID != draft.DepartmentID {
		t.Fatal("approved user has wrong name or department")
	}
	if err := a.assignRoleCallback(callback(999, "advisor")); err != nil {
		t.Fatal(err)
	}
	employee, err = db.GetEmployee(ctx, 201)
	if err != nil || employee.Role != "employee" {
		t.Fatal("reused approval button changed the role")
	}

	departments, err := db.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var otherDepartment int64
	for _, department := range departments {
		if department.Name != "IUTXRvaEQB" {
			otherDepartment = department.ID
		}
	}
	for _, entry := range []struct {
		id         int64
		name, role string
		department int64
	}{
		{301, "Bosh direktor maslahatchisi Foydalanuvchi", "advisor", draft.DepartmentID},
		{302, "Asosiy Boshliq", "manager", draft.DepartmentID},
		{303, "Boshqa Boshliq", "manager", otherDepartment},
		{202, "Topshirmagan Xodim", "employee", draft.DepartmentID},
		{203, "Boshqa Xodim", "employee", otherDepartment},
	} {
		if err := db.Approve(ctx, entry.id, entry.name, entry.role, 999); err != nil {
			t.Fatal(err)
		}
		if err := db.AssignDepartment(ctx, entry.id, entry.department); err != nil {
			t.Fatal(err)
		}
	}
	transport.messages = nil
	if err := a.saveReport(userContext(201, ""), "Bugungi bajarilgan ishlar"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"301", "302"} {
		if !strings.Contains(strings.Join(recipientMessages(transport, id), ""), "Bugungi bajarilgan ishlar") {
			t.Fatal("director or matching department head missed immediate report")
		}
	}
	if len(recipientMessages(transport, "303")) != 0 {
		t.Fatal("immediate report leaked to another department head")
	}
	now := time.Now().In(cfg.Timezone)
	before := time.Date(now.Year(), now.Month(), now.Day(), 18, 59, 0, 0, cfg.Timezone)
	if err := scheduler.QueueDailySummaries(ctx, db, cfg, before); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM notification_jobs WHERE kind='daily_summary'`).Scan(&queued); err != nil || queued != 0 {
		t.Fatal("daily summary queued before 19:00")
	}
	transport.messages = nil
	if err := scheduler.QueueDailySummaries(ctx, db, cfg, before.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	director := strings.Join(recipientMessages(transport, "301"), "")
	head := strings.Join(recipientMessages(transport, "302"), "")
	for _, text := range []string{director, head} {
		for _, part := range []string{"TOPSHIRGANLAR", "TOPSHIRMAGANLAR", "O‘ktam Rahimov", "Topshirmagan Xodim", "Bugungi bajarilgan ishlar"} {
			if !strings.Contains(text, part) {
				t.Fatalf("daily summary lacks %s", part)
			}
		}
	}
	if !strings.Contains(director, "Boshqa Xodim") || strings.Contains(head, "Boshqa Xodim") {
		t.Fatal("daily director/department-head scope is wrong")
	}
}

func TestRegistrationUsesSelectedNonDefaultDepartment(t *testing.T) {
	db := workflowDB(t)
	ctx := context.Background()
	departments, err := db.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected int64
	for _, department := range departments {
		if department.Name != "IUTXRvaEQB" {
			selected = department.ID
		}
	}
	if err := db.BeginRegistration(ctx, 401, "candidate"); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRegistrationName(ctx, 401, "Tanlangan Xodim"); err != nil {
		t.Fatal(err)
	}
	if err := db.SelectRegistrationDepartment(ctx, 401, selected); err != nil {
		t.Fatal(err)
	}
	if err := db.SubmitRegistration(ctx, 401, "manager"); err != nil {
		t.Fatal(err)
	}
	req, err := db.GetAccessRequest(ctx, 401)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApprovePending(ctx, 401, "manager", 999, req.RequestedAt.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	employee, err := db.GetEmployee(ctx, 401)
	if err != nil || employee.DepartmentID != selected {
		t.Fatal("approval overwrote selected department with default")
	}
}

func TestValidFullName(t *testing.T) {
	for _, name := range []string{"Anvar Buriboyev", "O‘tkir G‘ulomov", "Анвар Бурибоев"} {
		if !validFullName(name) {
			t.Fatal("valid full name rejected")
		}
	}
	for _, name := range []string{"Anvar", "123 456", "@spam link", "https://spam link"} {
		if validFullName(name) {
			t.Fatal("invalid name accepted")
		}
	}
}

func TestRejectedRequestAndStaleApproval(t *testing.T) {
	db := workflowDB(t)
	ctx := context.Background()
	departments, err := db.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	register := func(name string) {
		t.Helper()
		if err := db.BeginRegistration(ctx, 501, "candidate"); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveRegistrationName(ctx, 501, name); err != nil {
			t.Fatal(err)
		}
		if err := db.SelectRegistrationDepartment(ctx, 501, departments[0].ID); err != nil {
			t.Fatal(err)
		}
		if err := db.SubmitRegistration(ctx, 501, "employee"); err != nil {
			t.Fatal(err)
		}
	}
	register("Birinchi Familiya")
	first, err := db.GetAccessRequest(ctx, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RejectPending(ctx, 501, 999, first.RequestedAt.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	register("Ikkinchi Familiya")
	second, err := db.GetAccessRequest(ctx, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApprovePending(ctx, 501, "advisor", 999, first.RequestedAt.UnixMicro()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old rejected request approved a new submission")
	}
	if err := db.ApprovePending(ctx, 501, "employee", 999, second.RequestedAt.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	employee, err := db.GetEmployee(ctx, 501)
	if err != nil || employee.FullName != "Ikkinchi Familiya" || employee.Role != "employee" {
		t.Fatal("approval did not use current request")
	}
}
