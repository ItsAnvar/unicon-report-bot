package bot

import (
	tele "gopkg.in/telebot.v3"
	"net/http"
	"strings"
	"testing"
)

func TestGroupCommandsDoNotExposePrivateReports(t *testing.T) {
	transport := &memoryTelegram{}
	b, err := tele.NewBot(tele.Settings{Offline: true, Synchronous: true, Token: "test", URL: "https://offline.invalid", Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Bot: b}
	a.register()
	// No database is provided: any attempt to load reports/roles would panic.
	for _, command := range []string{"/reports", "/status", "/approve 101 advisor Person", "/deliveries"} {
		b.ProcessUpdate(tele.Update{Message: &tele.Message{Sender: &tele.User{ID: 999}, Chat: &tele.Chat{ID: -100, Type: tele.ChatGroup}, Text: command}})
	}
	if len(transport.messages) != 4 {
		t.Fatal("group commands were not blocked")
	}
	for _, message := range transport.messages {
		if !strings.Contains(message.Text, "shaxsiy chat") {
			t.Fatal("group received report data")
		}
	}
	b.ProcessUpdate(tele.Update{Message: &tele.Message{Sender: &tele.User{ID: 999}, Chat: &tele.Chat{ID: -100, Type: tele.ChatGroup}, Text: "ordinary group message"}})
	if len(transport.messages) != 4 {
		t.Fatal("ordinary group message should be ignored")
	}
}
