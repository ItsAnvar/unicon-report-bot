package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/store"
)

func managementApp(t *testing.T) (*App, *memoryTelegram, func(int64, string) tele.Context, func(int64, string) tele.Context) {
	t.Helper()
	db := workflowDB(t)
	transport := &memoryTelegram{}
	b, err := tele.NewBot(tele.Settings{Offline: true, Token: "test", URL: "https://offline.invalid", Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Bot: b, DB: db, Cfg: config.Config{AdminIDs: map[int64]bool{999: true}, Timezone: time.FixedZone("Asia/Tashkent", 18000)}}
	text := func(id int64, value string) tele.Context {
		return b.NewContext(tele.Update{Message: &tele.Message{Sender: &tele.User{ID: id}, Chat: &tele.Chat{ID: id, Type: tele.ChatPrivate}, Text: value}})
	}
	callback := func(id int64, value string) tele.Context {
		return b.NewContext(tele.Update{Callback: &tele.Callback{ID: "test", Sender: &tele.User{ID: id}, Message: &tele.Message{ID: 1, Chat: &tele.Chat{ID: id, Type: tele.ChatPrivate}}, Data: value}})
	}
	return app, transport, text, callback
}

func TestSixDirectorsRequireApprovalAndReceiveAllReports(t *testing.T) {
	a, transport, text, callback := managementApp(t)
	ctx := context.Background()
	departments, err := a.DB.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(701); id <= 706; id++ {
		for _, value := range []string{"/start", fmt.Sprintf("Bosh direktor maslahatchisi Familiya%d", id), "🏢 Bosh direktor maslahatchisi"} {
			if value == "/start" {
				err = a.start(text(id, value))
			} else {
				err = a.onText(text(id, value))
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := a.DB.GetAccessRequest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if req.RequestedRole != "advisor" || req.DepartmentID != 0 {
			t.Fatal("director request requires a department")
		}
		if _, err := a.DB.GetEmployee(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("director activated without approval")
		}
		if err := a.DB.ApprovePending(ctx, id, "employee", 999, req.RequestedAt.UnixMicro()); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("approval did not bind the requested role")
		}
		data := fmt.Sprintf("%d:advisor:%d", id, req.RequestedAt.UnixMicro())
		if err := a.assignRoleCallback(callback(888, data)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.DB.GetEmployee(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("non-admin approved director")
		}
		if err := a.assignRoleCallback(callback(999, data)); err != nil {
			t.Fatal(err)
		}
		employee, err := a.DB.GetEmployee(ctx, id)
		if err != nil || employee.Role != "advisor" || employee.DepartmentID != 0 {
			t.Fatal("approved director has wrong scope")
		}
	}
	for index, d := range departments {
		id := int64(801 + index)
		if err := a.DB.Approve(ctx, id, fmt.Sprintf("Xodim Familiya%d", id), "employee", 999); err != nil {
			t.Fatal(err)
		}
		if err := a.DB.AssignDepartment(ctx, id, d.ID); err != nil {
			t.Fatal(err)
		}
		if err := a.DB.SaveReport(ctx, id, time.Now().In(a.Cfg.Timezone), "Hisobot "+d.Name); err != nil {
			t.Fatal(err)
		}
	}
	transport.messages = nil
	if err := a.DeliverPending(ctx, 100); err != nil {
		t.Fatal(err)
	}
	for id := 701; id <= 706; id++ {
		messages := strings.Join(recipientMessages(transport, fmt.Sprint(id)), " ")
		for _, d := range departments {
			if !strings.Contains(messages, "Hisobot "+d.Name) {
				t.Fatalf("director %d missed department report", id)
			}
		}
	}
}

func TestAdminDepartmentAndRoleManagement(t *testing.T) {
	a, transport, text, callback := managementApp(t)
	ctx := context.Background()
	if err := a.start(text(999, "/start")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(transport.messages[len(transport.messages)-1].ReplyMarkup), "Bo‘lim qo‘shish") {
		t.Fatal("admin keyboard missing")
	}
	if err := a.onText(text(888, "➕ Bo‘lim qo‘shish")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.AdminAction(ctx, 888); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("non-admin began management action")
	}
	for _, value := range []string{"➕ Bo‘lim qo‘shish", "Buxgalteriya"} {
		if err := a.onText(text(999, value)); err != nil {
			t.Fatal(err)
		}
	}
	departments, err := a.DB.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, d := range departments {
		if d.Name == "Buxgalteriya" {
			id = d.ID
		}
	}
	if id == 0 {
		t.Fatal("department not created")
	}
	if err := a.managementCallback(callback(888, fmt.Sprintf("rename:%d", id))); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.AdminAction(ctx, 888); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("non-admin began rename")
	}
	if err := a.managementCallback(callback(999, fmt.Sprintf("rename:%d", id))); err != nil {
		t.Fatal(err)
	}
	// The prompt survives an App restart.
	restarted := &App{Bot: a.Bot, DB: a.DB, Cfg: a.Cfg}
	a = restarted
	if err := a.onText(text(999, "Moliya bo‘limi")); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Approve(ctx, 811, "Test Foydalanuvchi", "employee", 999); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.AssignDepartment(ctx, 811, id); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.SaveReport(ctx, 811, time.Now().In(a.Cfg.Timezone), "Oldingi hisobot"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.ManageDepartment(ctx, 999, "archive", id, ""); !errors.Is(err, store.ErrDepartmentBusy) {
		t.Fatal("occupied department archived")
	}
	if err := a.managementCallback(callback(888, "role:811:advisor")); err != nil {
		t.Fatal(err)
	}
	e, _ := a.DB.GetEmployee(ctx, 811)
	if e.Role != "employee" {
		t.Fatal("non-admin changed role")
	}
	for _, role := range []string{"manager", "employee", "advisor"} {
		if err := a.managementCallback(callback(999, "role:811:"+role)); err != nil {
			t.Fatal(err)
		}
		draft, err := a.DB.AdminAction(ctx, 999)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.managementCallback(callback(999, fmt.Sprintf("roleok:%s:%d", draft.Token, id))); err != nil {
			t.Fatal(err)
		}
		e, err = a.DB.GetEmployee(ctx, 811)
		if err != nil || e.Role != role {
			t.Fatal("admin role change failed")
		}
		if role == "advisor" && e.DepartmentID != 0 {
			t.Fatal("director still tied to department")
		}
		// A reused confirmation cannot replay a completed change.
		if _, err := a.DB.CompleteRoleAction(ctx, 999, draft.Token, id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("used confirmation accepted")
		}
	}
	if err := a.DB.ManageDepartment(ctx, 999, "archive", id, ""); err != nil {
		t.Fatal(err)
	}
	reports, err := a.DB.ReportsForDateByDepartment(ctx, time.Now().In(a.Cfg.Timezone), id)
	if err != nil || len(reports) != 1 || reports[0].ReportText != "Oldingi hisobot" {
		t.Fatal("role change or archival lost report history")
	}
	if err := a.managementCallback(callback(999, "role:811:revoke")); err != nil {
		t.Fatal(err)
	}
	draft, err := a.DB.AdminAction(ctx, 999)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.managementCallback(callback(999, "roleok:"+draft.Token+":0")); err != nil {
		t.Fatal(err)
	}
	e, err = a.DB.GetEmployee(ctx, 811)
	if err != nil || e.IsActive {
		t.Fatal("revocation did not disable access")
	}
	if deliveryAllowed(e, store.NotificationJob{}) {
		t.Fatal("revoked director can receive reports")
	}
	if err := a.DB.SaveReport(ctx, 811, time.Now(), "Denied"); err == nil {
		t.Fatal("revoked account can submit report")
	}
	reports, err = a.DB.ReportsForDateByDepartment(ctx, time.Now().In(a.Cfg.Timezone), id)
	if err != nil || len(reports) != 1 {
		t.Fatal("revocation hid saved report")
	}
	var audit int
	if err := a.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM management_audit`).Scan(&audit); err != nil || audit < 7 {
		t.Fatal("management audit missing")
	}
}

func TestMigrationJournalPreservesManagedDepartments(t *testing.T) {
	a, _, _, _ := managementApp(t)
	ctx := context.Background()
	if err := a.DB.EnsureDeliveryQueue(ctx); err != nil {
		t.Fatal(err)
	}
	departments, err := a.DB.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var combined int64
	for _, d := range departments {
		if d.Name == "Eri KORM" {
			combined = d.ID
		}
	}
	if err := a.DB.ManageDepartment(ctx, 999, "rename", combined, "Yangi nom"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.ManageDepartment(ctx, 999, "add", 0, "EQB"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.EnsureDeliveryQueue(ctx); err != nil {
		t.Fatal(err)
	}
	departments, err = a.DB.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, d := range departments {
		names[d.Name] = true
	}
	if !names["EQB"] || !names["Yangi nom"] || names["IUTXRvaEQB"] {
		t.Fatal("restart reran the legacy department merge")
	}
}

func TestStaleRoleActionAndPendingDepartmentCannotBeDeleted(t *testing.T) {
	a, _, _, _ := managementApp(t)
	ctx := context.Background()
	departments, err := a.DB.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dept := departments[0].ID
	if err := a.DB.BeginRegistration(ctx, 901, "test"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.SaveRegistrationName(ctx, 901, "Test Nomzod"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.SelectRegistrationDepartment(ctx, 901, dept); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.ManageDepartment(ctx, 999, "archive", dept, ""); !errors.Is(err, store.ErrDepartmentBusy) {
		t.Fatal("department with draft archived")
	}
	if err := a.DB.SubmitRegistration(ctx, 901, "manager"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.ManageDepartment(ctx, 999, "archive", dept, ""); !errors.Is(err, store.ErrDepartmentBusy) {
		t.Fatal("department with pending request archived")
	}
	req, err := a.DB.GetAccessRequest(ctx, 901)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.ApprovePending(ctx, 901, "manager", 999, req.RequestedAt.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	version, err := a.DB.EmployeeVersion(ctx, 901)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := a.DB.BeginAdminAction(ctx, 999, "role:advisor", 901, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.AssignDepartment(ctx, 901, dept); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.CompleteRoleAction(ctx, 999, draft.Token, 0); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("stale role menu accepted")
	}
}

func TestDiagnosticAfterManagementMigration(t *testing.T) {
	a, _, _, _ := managementApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := a.DB.EnsureDeliveryQueue(ctx); err != nil {
		t.Fatal(err)
	}
	departments, err := a.DB.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range departments {
		if err := a.DB.ManageDepartment(ctx, 999, "archive", d.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	dsn, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") {
		t.Skip("CLI diagnostic test requires a PostgreSQL URL")
	}
	query := dsn.Query()
	query.Set("search_path", a.DB.Pool.Config().ConnConfig.RuntimeParams["search_path"])
	dsn.RawQuery = query.Encode()
	command := exec.CommandContext(ctx, "go", "run", "./cmd/diagnose", "-skip-telegram")
	command.Dir = "../.."
	command.Env = append(os.Environ(), "DATABASE_URL="+dsn.String(), "BOT_TOKEN=offline-test", "ADMIN_TELEGRAM_IDS=999", "REPORT_GROUP_CHAT_ID=", "TIMEZONE=Asia/Tashkent")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated diagnostic failed: %s", output)
	}
	if strings.Contains(string(output), "FAIL:") || !strings.Contains(string(output), "OK: role and department management schema") {
		t.Fatalf("unexpected diagnostic output: %s", output)
	}
}

func TestLastAdminAndRevokedAdminCannotManage(t *testing.T) {
	a, _, text, callback := managementApp(t)
	ctx := context.Background()
	if err := a.DB.Approve(ctx, 912, "Test Administrator", "admin", 999); err != nil {
		t.Fatal(err)
	}
	version, err := a.DB.EmployeeVersion(ctx, 912)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := a.DB.BeginAdminAction(ctx, 999, "role:revoke", 912, version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.CompleteRoleAction(ctx, 999, draft.Token, 0); !errors.Is(err, store.ErrProtectedAdmin) {
		t.Fatal("last database administrator was removed")
	}
	if err := a.DB.Approve(ctx, 913, "Boshqa Administrator", "admin", 999); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.CompleteRoleAction(ctx, 999, draft.Token, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.onText(text(912, "➕ Bo‘lim qo‘shish")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.AdminAction(ctx, 912); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("revoked administrator can manage departments")
	}
	if err := a.managementCallback(callback(912, "role:913:advisor")); err != nil {
		t.Fatal(err)
	}
	e, err := a.DB.GetEmployee(ctx, 913)
	if err != nil || e.Role != "admin" {
		t.Fatal("revoked administrator can change roles")
	}
}
