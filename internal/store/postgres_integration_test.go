package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// Opt in locally: UNICON_DB_TEST=1 go test ./internal/store -v.
// All fixtures and migrations live in a temporary schema, never production tables.
func TestPostgresWorkflow(t *testing.T) {
	if os.Getenv("UNICON_DB_TEST") != "1" {
		t.Skip("set UNICON_DB_TEST=1 for isolated database integration test")
	}
	_ = godotenv.Load(filepath.Join("..", "..", ".env"))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal("database configuration invalid (details hidden)")
	}
	defer base.Close()
	schema := fmt.Sprintf("unicon_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("cannot create isolated test schema")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := base.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error("test schema cleanup failed")
		}
	}()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal("database configuration invalid")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("test pool failed")
	}
	defer pool.Close()
	s := &Store{Pool: pool}
	apply := func(name string) {
		t.Helper()
		sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("migration %s failed: %v", name, err)
		}
	}
	apply("001_init.sql")
	apply("002_departments.sql")
	apply("004_delivery_queue.sql")
	apply("005_onboarding_combined_department.sql")
	apply("006_roles_and_management.sql")
	if err := s.UpsertRequest(ctx, 101, "fixture", "Test employee"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, 101, "Test employee", "employee", 999); err != nil {
		t.Fatal(err)
	}
	// Reproduce the damaged legacy name actually encountered on Windows.
	if _, err := pool.Exec(ctx, `UPDATE departments SET name='Asosiy bo???lim'`); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetEmployee(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	apply("003_department_names.sql")
	apply("003_department_names.sql") // Re-running must not duplicate or lose data.
	departments, err := s.Departments(ctx)
	if err != nil || len(departments) != 2 {
		t.Fatal("expected exactly IUTXR and EQB")
	}
	ids := map[string]int64{}
	for _, d := range departments {
		ids[d.Name] = d.ID
	}
	if ids["IUTXR"] == 0 || ids["EQB"] == 0 {
		t.Fatal("department names missing")
	}
	after, err := s.GetEmployee(ctx, 101)
	if err != nil || after.DepartmentID != before.DepartmentID || after.DepartmentName != "IUTXR" {
		t.Fatal("migration did not preserve assignment")
	}
	if err := s.Approve(ctx, 102, "Other employee", "employee", 999); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignDepartment(ctx, 102, ids["EQB"]); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, 103, "Manager", "manager", 999); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, 104, "Advisor", "advisor", 999); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(time.FixedZone("Tashkent", 5*60*60))
	if err := s.SaveReport(ctx, 101, now, "First"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReport(ctx, 101, now, "Updated o‘📝"); err != nil {
		t.Fatal(err)
	}
	reports, err := s.ReportsForDateByDepartment(ctx, now, ids["IUTXR"])
	if err != nil || len(reports) != 1 || reports[0].ReportText != "Updated o‘📝" {
		t.Fatal("report upsert failed")
	}
	reports, err = s.ReportsForDateByDepartment(ctx, now, ids["EQB"])
	if err != nil || len(reports) != 0 {
		t.Fatal("report leaked into other department")
	}
	done, missing, err := s.StatusEmployeesByDepartment(ctx, now, 0)
	if err != nil || len(done) != 1 || len(missing) != 1 || missing[0].TelegramID != 102 {
		t.Fatal("status must count employees only")
	}
	// Changing a submitter's role must not hide an already saved report/status.
	if err := s.Approve(ctx, 101, "Test employee", "advisor", 999); err != nil {
		t.Fatal(err)
	}
	done, missing, err = s.StatusEmployeesByDepartment(ctx, now, 0)
	if err != nil || len(done) != 1 || len(missing) != 1 {
		t.Fatal("role change hid submitted status")
	}
	reports, err = s.ReportsForDateByDepartment(ctx, now, ids["IUTXR"])
	if err != nil || len(reports) != 1 {
		t.Fatal("role change hid report")
	}
	if err := s.Approve(ctx, 101, "Test employee", "employee", 999); err != nil {
		t.Fatal(err)
	}
	recipients, err := s.ReportRecipients(ctx)
	if err != nil || len(recipients) != 2 {
		t.Fatal("manager/advisor recipients missing")
	}
	requests, err := s.PendingRequests(ctx)
	if err != nil || len(requests) != 0 {
		t.Fatal("approval left pending request")
	}
	if err := s.SaveReport(ctx, 999999, now, "Invalid"); err == nil {
		t.Fatal("unknown user report must fail")
	}
	if _, err := pool.Exec(ctx, `UPDATE employees SET is_active=FALSE WHERE telegram_id=101`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReport(ctx, 101, now, "Inactive"); err == nil {
		t.Fatal("inactive user report must fail")
	}
	if err := s.AssignDepartment(ctx, 102, 999999); err == nil {
		t.Fatal("unknown department must fail")
	}
	apply("005_onboarding_combined_department.sql")
	var stalePayloads int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_jobs WHERE kind='report' AND payload->>'department_name'<>'IUTXRvaEQB'`).Scan(&stalePayloads); err != nil || stalePayloads != 0 {
		t.Fatal("queued report kept an old split department name")
	}
	if _, err := pool.Exec(ctx, `UPDATE notification_jobs SET message='cached message',next_part=1 WHERE id=(SELECT min(id) FROM notification_jobs)`); err != nil {
		t.Fatal(err)
	}
	apply("005_onboarding_combined_department.sql")
	var cached string
	if err := pool.QueryRow(ctx, `SELECT message FROM notification_jobs ORDER BY id LIMIT 1`).Scan(&cached); err != nil || cached != "cached message" {
		t.Fatal("repeated application migration reset delivery progress")
	}
	departments, err = s.Departments(ctx)
	if err != nil || len(departments) != 1 || departments[0].Name != "IUTXRvaEQB" {
		t.Fatal("combined department migration did not merge both legacy departments")
	}
	var oldReferences int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM employees e JOIN departments d ON d.id=e.department_id WHERE d.name<>'IUTXRvaEQB'`).Scan(&oldReferences); err != nil || oldReferences != 0 {
		t.Fatal("legacy assignments were not migrated")
	}
}
