package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	tele "gopkg.in/telebot.v3"
	"unicon-report-bot/internal/store"
)

func adminKeyboard() *tele.ReplyMarkup {
	return &tele.ReplyMarkup{ResizeKeyboard: true, ReplyKeyboard: [][]tele.ReplyButton{
		{{Text: "➕ Bo‘lim qo‘shish"}}, {{Text: "🏢 Bo‘limlarni boshqarish"}}, {{Text: "👥 Rollarni boshqarish"}}, {{Text: "📥 Kutilayotgan so‘rovlar"}},
	}}
}
func (a *App) adminMenu(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Send("Bu menyu faqat administrator uchun.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := a.DB.CancelAdminAction(ctx, c.Sender().ID); err != nil {
		return c.Send("Menyuni ochishda xatolik.")
	}
	return c.Send("⚙️ BOSHQARUV\nBo‘lim qo‘shish, nomini o‘zgartirish, o‘chirish va foydalanuvchi rollarini boshqarish uchun tugmani tanlang.\n/admin — ushbu menyu\n/reports — hisobotlar\n/status — hisobot holati", adminKeyboard())
}

func managementButton(text, data string) []tele.InlineButton {
	return []tele.InlineButton{{Unique: "management", Text: text, Data: data}}
}

func (a *App) departmentManagement(ctx context.Context, c tele.Context) error {
	departments, err := a.DB.Departments(ctx)
	if err != nil {
		return c.Send("Bo‘limlarni olishda xatolik.")
	}
	if len(departments) == 0 {
		return c.Send("Faol bo‘limlar yo‘q. «➕ Bo‘lim qo‘shish» tugmasidan foydalaning.", adminKeyboard())
	}
	for _, d := range departments {
		markup := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
			managementButton("✏️ Nomini o‘zgartirish", fmt.Sprintf("rename:%d", d.ID)),
			managementButton("🗑 Bo‘limni o‘chirish", fmt.Sprintf("archive:%d", d.ID)),
		}}
		if err := c.Send(fmt.Sprintf("🏢 %s\nID: %d", d.Name, d.ID), markup); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) userManagement(ctx context.Context, c tele.Context, offset int) error {
	users, err := a.DB.ManagedUsers(ctx, offset)
	if err != nil {
		return c.Send("Foydalanuvchilarni olishda xatolik.")
	}
	markup := &tele.ReplyMarkup{}
	for i, e := range users {
		if i == 8 {
			break
		}
		status := ""
		if !e.IsActive {
			status = " · faol emas"
		}
		markup.InlineKeyboard = append(markup.InlineKeyboard, managementButton(truncate(e.FullName, 50)+" · "+roleName(e.Role)+status, fmt.Sprintf("user:%d", e.TelegramID)))
	}
	if offset > 0 {
		markup.InlineKeyboard = append(markup.InlineKeyboard, managementButton("⬅️ Oldingi", fmt.Sprintf("users:%d", offset-8)))
	}
	if len(users) > 8 {
		markup.InlineKeyboard = append(markup.InlineKeyboard, managementButton("Keyingi ➡️", fmt.Sprintf("users:%d", offset+8)))
	}
	return c.Send("👥 ROLLARNI BOSHQARISH\nRolini ko‘rish yoki o‘zgartirish uchun foydalanuvchini tanlang.", markup)
}

func (a *App) handleManagementText(c tele.Context, text string) (bool, error) {
	// Authorization is checked on every input, including drafts created before revocation.
	switch text {
	case "➕ Bo‘lim qo‘shish", "🏢 Bo‘limlarni boshqarish", "👥 Rollarni boshqarish", "📥 Kutilayotgan so‘rovlar":
		if !a.isAdmin(c.Sender().ID) {
			return true, c.Send("Bu menyu faqat administrator uchun.")
		}
	}
	if !a.isAdmin(c.Sender().ID) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	switch text {
	case "➕ Bo‘lim qo‘shish":
		if _, err := a.DB.BeginAdminAction(ctx, c.Sender().ID, "add", 0, 0); err != nil {
			return true, c.Send("Amalni boshlashda xatolik.")
		}
		return true, c.Send("Yangi bo‘lim nomini yozing (2–80 belgi). Bekor qilish: /cancel.")
	case "🏢 Bo‘limlarni boshqarish":
		if err := a.DB.CancelAdminAction(ctx, c.Sender().ID); err != nil {
			return true, c.Send("Amalni bekor qilishda xatolik.")
		}
		return true, a.departmentManagement(ctx, c)
	case "👥 Rollarni boshqarish":
		if err := a.DB.CancelAdminAction(ctx, c.Sender().ID); err != nil {
			return true, c.Send("Amalni bekor qilishda xatolik.")
		}
		return true, a.userManagement(ctx, c, 0)
	case "📥 Kutilayotgan so‘rovlar":
		return true, a.requests(c)
	}
	draft, err := a.DB.AdminAction(ctx, c.Sender().ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, c.Send("Boshqaruv holatini olishda xatolik.")
	}
	if draft.Action != "add" && draft.Action != "rename" {
		return false, nil
	}
	name := strings.Join(strings.Fields(text), " ")
	if !store.ValidDepartmentName(name) {
		return true, c.Send("Bo‘lim nomi 2–80 belgidan iborat bo‘lsin. «Bosh direktor maslahatchisi» nomi rol uchun ajratilgan.")
	}
	if err := a.DB.ManageDepartment(ctx, c.Sender().ID, draft.Action, draft.TargetID, name); err != nil {
		return true, c.Send("Bo‘limni saqlab bo‘lmadi. Nom takrorlanmaganini va bo‘lim faol ekanini tekshiring.")
	}
	if err := a.DB.CancelAdminAction(ctx, c.Sender().ID); err != nil {
		return true, c.Send("Bo‘lim saqlandi. Davom etish uchun /admin yuboring.")
	}
	return true, c.Send("✅ Bo‘lim saqlandi: "+name, adminKeyboard())
}

func (a *App) managementCallback(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Respond(&tele.CallbackResponse{Text: "Faqat administrator uchun."})
	}
	if c.Callback() == nil {
		return nil
	}
	_ = c.Respond()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	parts := strings.Split(c.Callback().Data, ":")
	if len(parts) < 2 {
		return c.Send("Tugma noto‘g‘ri. /admin orqali yangilang.")
	}
	number := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	switch parts[0] {
	case "users":
		offset, err := strconv.Atoi(parts[1])
		if err != nil || offset < 0 {
			return c.Send("Sahifa noto‘g‘ri.")
		}
		return a.userManagement(ctx, c, offset)
	case "rename", "archive":
		id := number(parts[1])
		departments, err := a.DB.Departments(ctx)
		if err != nil {
			return c.Send("Bo‘limlarni olishda xatolik.")
		}
		name := ""
		for _, d := range departments {
			if d.ID == id {
				name = d.Name
			}
		}
		if name == "" {
			return c.Send("Bo‘lim topilmadi. /admin orqali yangilang.")
		}
		draft, err := a.DB.BeginAdminAction(ctx, c.Sender().ID, parts[0], id, 0)
		if err != nil {
			return c.Send("Amalni boshlashda xatolik.")
		}
		if parts[0] == "rename" {
			return c.Send("«" + name + "» bo‘limining yangi nomini yozing. Bekor qilish: /cancel.")
		}
		return c.Send("«"+name+"» bo‘limini o‘chirishni tasdiqlaysizmi? Tarixiy ma’lumotlar saqlanadi. Faol foydalanuvchilar yoki so‘rovlar bor bo‘lsa, o‘chirish bajarilmaydi.", &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{managementButton("🗑 Tasdiqlash", "archiveok:"+draft.Token)}})
	case "archiveok":
		draft, err := a.DB.AdminAction(ctx, c.Sender().ID)
		if err != nil || draft.Token != parts[1] || draft.Action != "archive" {
			return c.Send("Bu tasdiqlash eskirgan. /admin orqali yangilang.")
		}
		if err := a.DB.ManageDepartment(ctx, c.Sender().ID, "archive", draft.TargetID, ""); err != nil {
			if errors.Is(err, store.ErrDepartmentBusy) {
				return c.Send("Bo‘limda faol foydalanuvchi yoki tugallanmagan so‘rov bor. Avval ularni boshqa bo‘limga o‘tkazing yoki so‘rovlarni hal qiling.")
			}
			return c.Send("Bo‘limni o‘chirishda xatolik.")
		}
		_ = a.DB.CancelAdminAction(ctx, c.Sender().ID)
		return c.Edit("✅ Bo‘lim faol ro‘yxatdan olib tashlandi. Tarixiy ma’lumotlar saqlandi.", &tele.ReplyMarkup{})
	case "user":
		id := number(parts[1])
		e, err := a.DB.GetEmployee(ctx, id)
		if err != nil {
			return c.Send("Foydalanuvchi topilmadi.")
		}
		department := e.DepartmentName
		if e.Role == "advisor" {
			department = "Barcha bo‘limlar"
		}
		message := fmt.Sprintf("👤 %s\nTelegram ID: %d\nRol: %s\nBo‘lim: %s\nFaol: %t", e.FullName, e.TelegramID, roleName(e.Role), department, e.IsActive)
		if a.Cfg.AdminIDs[id] || id == c.Sender().ID {
			return c.Send(message + "\nBu administratorni ushbu menyudan o‘zgartirish mumkin emas.")
		}
		markup := &tele.ReplyMarkup{}
		for _, role := range []string{"employee", "manager", "advisor", "revoke"} {
			label := roleName(role)
			if role == "revoke" {
				label = "⛔ Rolni olib qo‘yish"
			}
			markup.InlineKeyboard = append(markup.InlineKeyboard, managementButton(label, fmt.Sprintf("role:%d:%s", id, role)))
		}
		return c.Send(message+"\nYangi rolni tanlang:", markup)
	case "role":
		if len(parts) != 3 {
			return c.Send("Tugma noto‘g‘ri.")
		}
		id, role := number(parts[1]), parts[2]
		if a.Cfg.AdminIDs[id] || id == c.Sender().ID {
			return c.Send("Bu administrator himoyalangan.")
		}
		if role != "employee" && role != "manager" && role != "advisor" && role != "revoke" {
			return c.Send("Rol noto‘g‘ri.")
		}
		e, err := a.DB.GetEmployee(ctx, id)
		if err != nil {
			return c.Send("Foydalanuvchi topilmadi.")
		}
		version, err := a.DB.EmployeeVersion(ctx, id)
		if err != nil {
			return c.Send("Rol holatini olishda xatolik.")
		}
		draft, err := a.DB.BeginAdminAction(ctx, c.Sender().ID, "role:"+role, id, version)
		if err != nil {
			return c.Send("Amalni boshlashda xatolik.")
		}
		markup := &tele.ReplyMarkup{}
		if role == "advisor" || role == "revoke" {
			label := "Bosh direktor maslahatchisi rolini berish"
			if role == "revoke" {
				label = "Rol va kirish huquqini olib qo‘yish"
			}
			markup.InlineKeyboard = append(markup.InlineKeyboard, managementButton("✅ Tasdiqlash", "roleok:"+draft.Token+":0"))
			return c.Send(e.FullName+" — "+label+"? Bekor qilish: /cancel.", markup)
		}
		departments, err := a.DB.Departments(ctx)
		if err != nil {
			return c.Send("Bo‘limlarni olishda xatolik.")
		}
		if len(departments) == 0 {
			return c.Send("Avval bo‘lim qo‘shing.")
		}
		for _, d := range departments {
			markup.InlineKeyboard = append(markup.InlineKeyboard, managementButton(d.Name, fmt.Sprintf("roleok:%s:%d", draft.Token, d.ID)))
		}
		return c.Send(e.FullName+" — yangi rol: "+roleName(role)+". Tasdiqlash uchun bo‘limni tanlang:", markup)
	case "roleok":
		if len(parts) != 3 {
			return c.Send("Tugma noto‘g‘ri.")
		}
		draft, err := a.DB.AdminAction(ctx, c.Sender().ID)
		if err != nil || draft.Token != parts[1] || a.Cfg.AdminIDs[draft.TargetID] {
			return c.Send("Bu amal eskirgan yoki foydalanuvchi himoyalangan. /admin orqali yangilang.")
		}
		id, err := a.DB.CompleteRoleAction(ctx, c.Sender().ID, parts[1], number(parts[2]))
		if err != nil {
			return c.Send("Rolni o‘zgartirib bo‘lmadi. Holat o‘zgargan, bo‘lim faol emas yoki administrator himoyalangan. /admin orqali yangilang.")
		}
		a.reportWait.Delete(id)
		if draft.Action != "role:revoke" {
			if err := a.queueLeaderOverview(ctx, id); err != nil {
				return c.Edit("✅ Rol o‘zgartirildi. Hisobotni ko‘rish uchun foydalanuvchi /start yuborsin.", &tele.ReplyMarkup{})
			}
		}
		return c.Edit("✅ Foydalanuvchining roli yangilandi. O‘zgarish darhol kuchga kirdi.", &tele.ReplyMarkup{})
	}
	return c.Send("Tugma eskirgan. /admin orqali yangilang.")
}
