package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/store"
)

type capturedMessage struct {
	ChatID      string          `json:"chat_id"`
	Text        string          `json:"text"`
	ReplyMarkup json.RawMessage `json:"reply_markup"`
}
type memoryTelegram struct {
	messages      []capturedMessage
	fail          bool
	attempts      map[string]int
	failOnAttempt map[string]int
}

// A transport implemented entirely in memory; no socket or Telegram API is used.
func (m *memoryTelegram) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "offline.invalid" {
		return nil, fmt.Errorf("external network forbidden in test")
	}
	var message capturedMessage
	if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
		return nil, err
	}
	if m.attempts == nil {
		m.attempts = map[string]int{}
	}
	m.attempts[message.ChatID]++
	if m.fail || (m.failOnAttempt[message.ChatID] > 0 && m.attempts[message.ChatID] == m.failOnAttempt[message.ChatID]) {
		return nil, fmt.Errorf("simulated delivery failure")
	}
	m.messages = append(m.messages, message)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1,"chat":{"id":1,"type":"private"}}}`))}, nil
}

func workflowDB(t *testing.T) *store.Store {
	t.Helper()
	if os.Getenv("UNICON_DB_TEST") != "1" {
		t.Skip("set UNICON_DB_TEST=1 for database workflow tests")
	}
	_ = godotenv.Load(filepath.Join("..", "..", ".env"))
	ctx := context.Background()
	base, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	schema := fmt.Sprintf("unicon_bot_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		base.Close()
		t.Fatal("cannot create isolated test schema")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := base.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error("test schema cleanup failed")
		}
		base.Close()
	})
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid database configuration")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("test pool failed")
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"001_init.sql", "002_departments.sql", "003_department_names.sql", "004_delivery_queue.sql", "005_onboarding_combined_department.sql", "006_roles_and_management.sql"} {
		sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	// A synthetic second department only exists inside this isolated test schema.
	if _, err := pool.Exec(ctx, `INSERT INTO departments(name) VALUES('Boshqa bo‘lim')`); err != nil {
		t.Fatal(err)
	}
	return &store.Store{Pool: pool}
}

func TestLeaderWorkflowOffline(t *testing.T) {
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
		id                     int64
		name, role, department string
	}{
		{101, "IUTXR employee", "employee", "IUTXRvaEQB"},
		{102, "EQB employee", "employee", "Boshqa bo‘lim"},
		{103, "IUTXR manager", "manager", "IUTXRvaEQB"},
		{104, "EQB manager", "manager", "Boshqa bo‘lim"},
		{105, "Advisor", "advisor", "IUTXRvaEQB"},
	} {
		if err := db.Approve(ctx, e.id, e.name, e.role, 999); err != nil {
			t.Fatal(err)
		}
		if err := db.AssignDepartment(ctx, e.id, ids[e.department]); err != nil {
			t.Fatal(err)
		}
	}
	transport := &memoryTelegram{}
	b, err := tele.NewBot(tele.Settings{Offline: true, URL: "https://offline.invalid", Token: "test", Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("Asia/Tashkent", 5*60*60)
	a := &App{Bot: b, DB: db, Cfg: config.Config{Timezone: loc, AdminIDs: map[int64]bool{105: true}}}
	contextFor := func(id int64) tele.Context {
		return b.NewContext(tele.Update{Message: &tele.Message{Sender: &tele.User{ID: id}, Chat: &tele.Chat{ID: id, Type: tele.ChatPrivate}}})
	}
	if err := a.saveReport(contextFor(101), "IUTXR secret report"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeliverPending(ctx, 64); err != nil {
		t.Fatal(err)
	}
	sent := map[string]string{}
	for _, msg := range transport.messages {
		sent[msg.ChatID] += msg.Text
	}
	for _, id := range []string{"103", "105"} {
		if !strings.Contains(sent[id], "IUTXR secret report") || !strings.Contains(sent[id], "Topshirgan: 1") || !strings.Contains(sent[id], "IUTXR employee") {
			t.Fatalf("recipient %s did not get automatic report and status", id)
		}
	}
	if sent["104"] != "" {
		t.Fatal("IUTXR report leaked to EQB manager")
	}
	transport.messages = nil
	if err := a.status(contextFor(105)); err != nil {
		t.Fatal(err)
	}
	if len(transport.messages) == 0 || !strings.Contains(transport.messages[0].Text, "Topshirgan: 1") || !strings.Contains(transport.messages[0].Text, "IUTXR employee") {
		t.Fatal("advisor status hid submitted employee")
	}
	transport.messages = nil
	if err := a.start(contextFor(105)); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, message := range transport.messages {
		text.WriteString(message.Text)
	}
	if !strings.Contains(text.String(), "IUTXR secret report") || !strings.Contains(text.String(), "TOPSHIRGANLAR") {
		t.Fatal("advisor start did not display reports automatically")
	}
	transport.messages = nil
	if err := a.start(contextFor(104)); err != nil {
		t.Fatal(err)
	}
	text.Reset()
	for _, message := range transport.messages {
		text.WriteString(message.Text)
	}
	if strings.Contains(text.String(), "IUTXR secret report") || !strings.Contains(text.String(), "EQB employee") {
		t.Fatal("manager start has wrong department scope")
	}
	// A report remains visible after its author's role changes.
	if err := db.Approve(ctx, 101, "IUTXR employee", "manager", 999); err != nil {
		t.Fatal(err)
	}
	transport.messages = nil
	if err := a.status(contextFor(105)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transport.messages[0].Text, "Topshirgan: 1") {
		t.Fatal("role change hid submitted report")
	}
}

func TestStatusAlwaysShowsBothLists(t *testing.T) {
	text := formatStatus(time.Now(), nil, []store.Employee{{FullName: "Missing employee"}}, "IUTXRvaEQB", false)
	if !strings.Contains(text, "✅ Topshirganlar:\nYo‘q.") || !strings.Contains(text, "Missing employee") {
		t.Fatal("empty submitted list must be explicit")
	}
}
