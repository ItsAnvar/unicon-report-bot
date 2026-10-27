package bot

import (
	tele "gopkg.in/telebot.v3"
	"strings"
)

// Employee reports, status and role administration belong in private chats.
// The configured group still receives announcements through explicit Bot.Send.
func privateChatOnly(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		chat := c.Chat()
		if chat == nil {
			return nil
		}
		if chat.Type == tele.ChatPrivate {
			return next(c)
		}
		if c.Callback() != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Bu amalni botning shaxsiy chatida bajaring."})
		}
		if strings.HasPrefix(c.Text(), "/") {
			return c.Send("Hisobot va boshqaruv buyruqlari uchun botning shaxsiy chatini oching.")
		}
		return nil
	}
}
