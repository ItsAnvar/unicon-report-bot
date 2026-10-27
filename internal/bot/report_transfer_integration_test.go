package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReportResubmittedAfterDepartmentTransfer(t *testing.T) {
	a, transport, original := deliveryFixture(t)
	ctx := context.Background()
	now := time.Now().In(a.Cfg.Timezone)
	employee, err := a.DB.GetEmployee(ctx, 102)
	if err != nil {
		t.Fatal(err)
	}
	destination := employee.DepartmentID
	if err := a.DB.SaveReport(ctx, 101, now, "Old department report"); err != nil {
		t.Fatal(err)
	}
	// Include an unsent cached summary from before the department transfer.
	manager, err := a.DB.GetEmployee(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.QueueLeaderSummary(ctx, manager, now, "daily_summary"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Pool.Exec(ctx, `UPDATE notification_jobs SET message='Old department report',message_scope=$1 WHERE kind='daily_summary'`, fmt.Sprintf("manager:%d", original)); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.AssignDepartment(ctx, 101, destination); err != nil {
		t.Fatal(err)
	}
	// Merely moving an account preserves its previous submission's department.
	before, err := a.DB.ReportsForDateByDepartment(ctx, now, original)
	if err != nil || len(before) != 1 {
		t.Fatal("transfer changed historical report before resubmission")
	}
	if err := a.DB.SaveReport(ctx, 101, now, "New department report"); err != nil {
		t.Fatal(err)
	}
	oldReports, err := a.DB.ReportsForDateByDepartment(ctx, now, original)
	if err != nil || len(oldReports) != 0 {
		t.Fatal("former department still sees revised report")
	}
	newReports, err := a.DB.ReportsForDateByDepartment(ctx, now, destination)
	if err != nil || len(newReports) != 1 || newReports[0].ReportText != "New department report" {
		t.Fatal("new department cannot see revised report")
	}
	// Reconciliation must not resurrect pending jobs for the former department.
	if err := a.DB.QueueReportsForDate(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 100); err != nil {
		t.Fatal(err)
	}
	oldMessages := strings.Join(recipientMessages(transport, "103"), " ")
	if strings.Contains(oldMessages, "New department report") || strings.Contains(oldMessages, "Old department report") {
		t.Fatal("former head received a stale job or cached summary")
	}
	for _, id := range []string{"104", "105", "106", "107"} {
		if !strings.Contains(strings.Join(recipientMessages(transport, id), " "), "New department report") {
			t.Fatalf("authorized recipient %s missed revised report", id)
		}
	}
}
