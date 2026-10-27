package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/store"
)

func roleName(role string) string {
	switch role {
	case "employee":
		return "Xodim"
	case "manager":
		return "Bo‘lim boshlig‘i"
	case "advisor":
		return "Bosh direktor maslahatchisi"
	case "admin":
		return "Administrator"
	}
	return role
}

func (a *App) beginOnboarding(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if req, err := a.DB.GetAccessRequest(ctx, c.Sender().ID); err == nil {
		return c.Send(fmt.Sprintf("⏳ So‘rovingiz admin tasdig‘ini kutmoqda.\nBo‘lim: %s\nIsm-familiya: %s\nRol: %s", requestDepartment(req), req.RequestedName, roleName(req.RequestedRole)), &tele.ReplyMarkup{RemoveKeyboard: true})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return c.Send("Ro‘yxatdan o‘tishni tekshirishda xatolik. /start orqali qayta urinib ko‘ring.")
	}
	draft, err := a.DB.RegistrationDraft(ctx, c.Sender().ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := a.DB.BeginRegistration(ctx, c.Sender().ID, c.Sender().Username); err != nil {
			return c.Send("Ro‘yxatdan o‘tishni boshlashda xatolik.")
		}
		draft.Step = "name"
	} else if err != nil {
		return c.Send("Ro‘yxatdan o‘tishni tekshirishda xatolik.")
	}
	if draft.Step == "name" {
		return c.Send("Ism va familiyangizni to‘liq kiriting:", &tele.ReplyMarkup{RemoveKeyboard: true})
	}
	if draft.Step == "role" {
		return showRegistrationRoles(c, draft.DepartmentName)
	}
	return a.showDepartmentChoices(ctx, c)
}

func (a *App) showDepartmentChoices(ctx context.Context, c tele.Context) error {
	departments, err := a.DB.Departments(ctx)
	if err != nil {
		return c.Send("Bo‘limlar ro‘yxatini olishda xatolik.")
	}
	markup := &tele.ReplyMarkup{ResizeKeyboard: true, OneTimeKeyboard: true}
	for _, department := range departments {
		markup.ReplyKeyboard = append(markup.ReplyKeyboard, []tele.ReplyButton{{Text: department.Name}})
	}
	markup.ReplyKeyboard = append(markup.ReplyKeyboard, []tele.ReplyButton{{Text: "🏢 Bosh direktor maslahatchisi"}})
	return c.Send("Bo‘limingizni tanlang. Bosh direktor maslahatchisi bo‘lsangiz, eng pastdagi «🏢 Bosh direktor maslahatchisi» tugmasini bosing:", markup)
}

func showRegistrationRoles(c tele.Context, department string) error {
	return c.Send("🏢 Bo‘lim: "+department+"\nRolingizni tanlang. Rol faqat admin tasdiqlagandan keyin beriladi:", &tele.ReplyMarkup{ResizeKeyboard: true, OneTimeKeyboard: true, ReplyKeyboard: [][]tele.ReplyButton{{{Text: "👔 Bo‘lim boshlig‘i"}}, {{Text: "👤 Xodim"}}}})
}

func requestDepartment(req store.AccessRequest) string {
	if req.RequestedRole == "advisor" {
		return "Barcha bo‘limlar"
	}
	return req.DepartmentName
}

func (a *App) handleOnboardingText(c tele.Context, text string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	draft, err := a.DB.RegistrationDraft(ctx, c.Sender().ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, c.Send("Ro‘yxatdan o‘tishni tekshirishda xatolik. Qayta urinib ko‘ring.")
	}
	if draft.Step == "name" {
		name := strings.Join(strings.Fields(text), " ")
		if !validFullName(name) {
			return true, c.Send("Iltimos, ism va familiyangizni to‘liq yozing. Masalan: Anvar Buriboyev (120 belgigacha).")
		}
		if err := a.DB.SaveRegistrationName(ctx, c.Sender().ID, name); err != nil {
			return true, c.Send("Ismni saqlashda xatolik. /start orqali qayta urinib ko‘ring.")
		}
		return true, a.showDepartmentChoices(ctx, c)
	}
	selectedRole := ""
	if draft.Step == "department" {
		if text == "🏢 Bosh direktor maslahatchisi" || text == "🏢 Direktor" {
			selectedRole = "advisor"
		} else {
			departments, err := a.DB.Departments(ctx)
			if err != nil {
				return true, c.Send("Bo‘limlarni olishda xatolik.")
			}
			for _, department := range departments {
				if strings.EqualFold(strings.TrimSpace(text), department.Name) {
					if err := a.DB.SelectRegistrationDepartment(ctx, c.Sender().ID, department.ID); err != nil {
						return true, c.Send("Bo‘limni saqlashda xatolik. /start orqali qayta urinib ko‘ring.")
					}
					return true, showRegistrationRoles(c, department.Name)
				}
			}
			return true, c.Send("Iltimos, pastdagi tugmadan mavjud bo‘limni tanlang.")
		}
	}
	if draft.Step == "role" {
		switch text {
		case "👔 Bo‘lim boshlig‘i":
			selectedRole = "manager"
		case "👤 Xodim":
			selectedRole = "employee"
		default:
			return true, showRegistrationRoles(c, draft.DepartmentName)
		}
	}
	if err := a.DB.SubmitRegistration(ctx, c.Sender().ID, selectedRole); err != nil {
		return true, c.Send("So‘rovni saqlashda xatolik. /start orqali holatni tekshiring.")
	}
	req, err := a.DB.GetAccessRequest(ctx, c.Sender().ID)
	if err != nil {
		return true, c.Send("So‘rov saqlandi. Admin tasdig‘ini kuting.")
	}
	return true, a.notifyAdminsForRole(c, req)
}

func validFullName(name string) bool {
	parts := strings.Fields(name)
	if len(parts) < 2 || len([]rune(name)) > 120 {
		return false
	}
	for _, part := range parts {
		letters := false
		for _, r := range part {
			if unicode.IsLetter(r) {
				letters = true
			}
			if unicode.IsControl(r) {
				return false
			}
		}
		if !letters {
			return false
		}
	}
	return !strings.ContainsAny(name, "/@") && !strings.Contains(name, "://")
}

func requestApprovalMessage(req store.AccessRequest) (string, *tele.ReplyMarkup) {
	markup := &tele.ReplyMarkup{}
	for _, entry := range []struct{ role, label string }{{req.RequestedRole, "✅ Tasdiqlash"}, {"reject", "❌ Rad etish"}} {
		markup.InlineKeyboard = append(markup.InlineKeyboard, []tele.InlineButton{{Unique: "assign_role", Text: entry.label, Data: fmt.Sprintf("%d:%s:%d", req.TelegramID, entry.role, req.RequestedAt.UnixMicro())}})
	}
	message := fmt.Sprintf("📥 TASDIQLASH UCHUN SO‘ROV\n🏢 Bo‘lim: %s\n👤 Ism-familiya: %s\n🎖 So‘ralgan rol: %s\nTelegram ID: %d\nUsername: @%s\n\nSo‘rovni tasdiqlang yoki rad eting:", requestDepartment(req), req.RequestedName, roleName(req.RequestedRole), req.TelegramID, req.Username)
	return message, markup
}

func (a *App) notifyAdminsForRole(c tele.Context, req store.AccessRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ids, err := a.DB.ActiveAdminIDs(ctx)
	if err != nil {
		return c.Send("So‘rov saqlandi. Adminga xabar yuborishda xatolik; /requests orqali tekshiriladi.", &tele.ReplyMarkup{RemoveKeyboard: true})
	}
	admins := map[int64]bool{}
	for id := range a.Cfg.AdminIDs {
		admins[id] = true
	}
	for _, id := range ids {
		admins[id] = true
	}
	message, markup := requestApprovalMessage(req)
	sent := 0
	for id := range admins {
		if _, err := a.Bot.Send(&tele.User{ID: id}, message, markup); err != nil {
			log.Print("registration approval notification failed (details hidden)")
		} else {
			sent++
		}
	}
	if sent == 0 {
		return c.Send("So‘rovingiz saqlandi. Administrator /requests orqali uni ko‘rib tasdiqlashi kerak.", &tele.ReplyMarkup{RemoveKeyboard: true})
	}
	return c.Send("✅ Ism-familiya va tanlagan rolingiz saqlandi. So‘rov adminga yuborildi. Tasdiqlashini kuting.", &tele.ReplyMarkup{RemoveKeyboard: true})
}
