package bot

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"unicon-report-bot/internal/config"
	"unicon-report-bot/internal/scheduler"
	"unicon-report-bot/internal/store"

	tele "gopkg.in/telebot.v3"
)

type App struct {
	Bot        *tele.Bot
	DB         *store.Store
	Cfg        config.Config
	reportWait sync.Map // telegram ID -> bool
}

func New(cfg config.Config, db *store.Store) (*App, error) {
	b, err := tele.NewBot(tele.Settings{
		Token:   cfg.BotToken,
		Poller:  &tele.LongPoller{Timeout: 10 * time.Second},
		Client:  &http.Client{Timeout: 15 * time.Second},
		OnError: func(err error, c tele.Context) { log.Print("bot handler failed (details hidden)") },
	})
	if err != nil {
		return nil, err
	}
	a := &App{Bot: b, DB: db, Cfg: cfg}
	a.register()
	return a, nil
}

func (a *App) Start() { a.Bot.Start() }

func (a *App) register() {
	a.Bot.Use(privateChatOnly)
	a.Bot.Handle("/admin", a.adminMenu)
	a.Bot.Handle(&tele.InlineButton{Unique: "management"}, a.managementCallback)
	a.Bot.Handle("/departments", a.departments)
	a.Bot.Handle("/start", a.start)
	a.Bot.Handle("/help", a.help)
	a.Bot.Handle("/request", a.requestAccess)
	a.Bot.Handle("/report", a.reportCommand)
	a.Bot.Handle("/cancel", a.cancelReport)
	a.Bot.Handle("/myreports", a.myReports)
	a.Bot.Handle("/reports", a.reports)
	a.Bot.Handle("/status", a.status)
	a.Bot.Handle("/whoami", a.whoAmI)
	a.Bot.Handle("/version", func(c tele.Context) error { return c.Send("UNICON Report Bot 2026-10-19.2") })
	a.Bot.Handle("/deliveries", a.deliveries)
	a.Bot.Handle("/retrydeliveries", a.retryDeliveries)
	a.Bot.Handle("/requests", a.requests)
	a.Bot.Handle("/approve", a.approve)
	a.Bot.Handle("/setdept", a.setDepartment)
	a.Bot.Handle(&tele.InlineButton{Unique: "assign_role"}, a.assignRoleCallback)
	a.Bot.Handle(tele.OnText, a.onText)
}

func (a *App) start(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}
	a.reportWait.Delete(c.Sender().ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.DB.RememberUsername(ctx, c.Sender().ID, c.Sender().Username); err != nil {
		log.Print("remember username failed (details hidden)")
	}
	if err := a.DB.WakeNotifications(ctx, c.Sender().ID); err != nil {
		log.Print("wake deliveries failed (details hidden)")
	}
	e, err := a.DB.GetEmployee(ctx, c.Sender().ID)
	if a.isAdmin(c.Sender().ID) {
		if menuErr := a.adminMenu(c); menuErr != nil {
			return menuErr
		}
		if err != nil || !e.IsActive || (e.Role != "manager" && e.Role != "advisor") {
			return nil
		}
	}

	if err == nil && e.IsActive {
		if err := c.Send(a.welcomeForRole(e)); err != nil {
			return err
		}
		if e.Role == "manager" || e.Role == "advisor" {
			departmentID := int64(0)
			if e.Role == "manager" {
				if e.DepartmentID == 0 {
					return c.Send("Sizga bo‘lim biriktirilmagan. Administrator /setdept orqali bo‘limni belgilashi kerak.")
				}
				departmentID = e.DepartmentID
			}
			summaryCtx, summaryCancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer summaryCancel()
			message, err := scheduler.BuildDailySummary(summaryCtx, a.DB, time.Now().In(a.Cfg.Timezone), departmentID, e.DepartmentName, e.Role == "advisor")
			if err != nil {
				log.Printf("start summary: %v", err)
				return c.Send("Bugungi hisobotlarni olishda xatolik. /status orqali qayta urinib ko‘ring.")
			}
			return sendLong(c, message)
		}
		return nil
	}

	return a.beginOnboarding(c)
}
func (a *App) welcomeForRole(e store.Employee) string {
	switch e.Role {
	case "manager":
		return fmt.Sprintf("Assalomu alaykum, %s!\nRol: Bo‘lim boshlig‘i\n\nO‘z bo‘limingizning yangi hisobotlari darhol, kunlik yakun esa 19:00 da avtomatik keladi.\n📊 Hisobotlarni qo‘lda ko‘rish: /reports\n📋 Topshirgan/topshirmaganlar: /status\n📝 O‘z hisobotingiz: /report\n📚 Hisobotlarim: /myreports", e.FullName)
	case "advisor":
		return fmt.Sprintf("Assalomu alaykum, %s!\nRol: Bosh direktor maslahatchisi\n\nBarcha bo‘limlarning yangi hisobotlari darhol, kunlik yakun esa 19:00 da avtomatik keladi.\n📊 Hisobotlarni qo‘lda ko‘rish: /reports\n📋 Topshirgan/topshirmaganlar: /status\n📝 O‘z hisobotingiz: /report\n📚 Hisobotlarim: /myreports", e.FullName)
	case "admin":
		return fmt.Sprintf("Assalomu alaykum, %s!\nRol: Admin\n\n/requests — so‘rovlar\n/departments — bo‘limlar\n/reports — hisobotlar\n/status — hisobot holati", e.FullName)
	default:
		return fmt.Sprintf("Assalomu alaykum, %s!\nRol: Employee\n\n📝 Bugungi hisobot: /report\n📚 Hisobotlarim: /myreports\n❓ Yordam: /help", e.FullName)
	}
}

func (a *App) assignRoleCallback(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Respond(&tele.CallbackResponse{Text: "Faqat administrator rol bera oladi."})
	}
	if c.Callback() == nil {
		return nil
	}
	parts := strings.Split(c.Callback().Data, ":")
	if len(parts) != 3 {
		return c.Respond(&tele.CallbackResponse{Text: "Tugma ma’lumoti noto‘g‘ri."})
	}
	targetID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || targetID <= 0 {
		return c.Respond(&tele.CallbackResponse{Text: "Telegram ID noto‘g‘ri."})
	}
	role := strings.ToLower(parts[1])
	version, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Eski yoki noto‘g‘ri tugma. /requests orqali yangilang."})
	}
	if role != "employee" && role != "manager" && role != "advisor" && role != "reject" {
		return c.Respond(&tele.CallbackResponse{Text: "Bu rolga ruxsat yo‘q."})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := a.DB.GetAccessRequest(ctx, targetID)
	if err != nil {
		log.Printf("get access request: %v", err)
		return c.Respond(&tele.CallbackResponse{Text: "So‘rov topilmadi. Foydalanuvchi /start ni qayta bossin."})
	}
	name := strings.TrimSpace(req.RequestedName)
	if name == "" {
		name = "Telegram foydalanuvchisi"
	}
	if version != req.RequestedAt.UnixMicro() {
		return c.Respond(&tele.CallbackResponse{Text: "Bu tugma eski so‘rovga tegishli. /requests orqali yangilang."})
	}
	if role == "reject" {
		if err := a.DB.RejectPending(ctx, targetID, c.Sender().ID, version); err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "So‘rov allaqachon ko‘rib chiqilgan."})
		}
		_, _ = a.Bot.Send(&tele.User{ID: targetID}, "So‘rovingiz administrator tomonidan rad etildi. To‘g‘ri ma’lumot bilan qayta ro‘yxatdan o‘tish uchun /start yuboring.")
		_ = c.Respond(&tele.CallbackResponse{Text: "So‘rov rad etildi."})
		return c.Edit("❌ "+name+" — so‘rov rad etildi.", &tele.ReplyMarkup{})
	}
	if err := a.DB.ApprovePending(ctx, targetID, role, c.Sender().ID, version); err != nil {
		log.Printf("approve via button: %v", err)
		return c.Respond(&tele.CallbackResponse{Text: "Rol berishda xatolik yuz berdi."})
	}
	if err := a.queueLeaderOverview(ctx, targetID); err != nil {
		log.Print("queue approved leader overview failed (details hidden)")
	}

	roleLabel := roleName(role)
	_, sendErr := a.Bot.Send(&tele.User{ID: targetID}, "✅ Ro‘yxatdan o‘tishingiz tasdiqlandi!\n🏢 Bo‘lim: "+requestDepartment(req)+"\n\n"+a.welcomeForRole(store.Employee{FullName: name, Role: role, IsActive: true}), &tele.ReplyMarkup{RemoveKeyboard: true})
	if sendErr != nil {
		log.Printf("notify newly approved user %d failed (details hidden)", targetID)
	}
	_ = c.Respond(&tele.CallbackResponse{Text: name + " — " + roleLabel + " roli berildi."})
	_ = c.Edit(fmt.Sprintf("✅ %s (ID: %d)\nBo‘lim: %s\nRol: %s", name, targetID, requestDepartment(req), roleLabel), &tele.ReplyMarkup{})
	return nil
}

func (a *App) help(c tele.Context) error {
	return c.Send("Buyruqlar:\n/request — tizimga kirish uchun so‘rov\n/report — bugungi hisobotni yuborish\n/myreports — oxirgi hisobotlaringiz\n/reports — rahbarlar uchun bugungi hisobotlar\n/status — kim topshirgan/topshirmagan\n\nHisobot yuborish: /report buyrug‘idan keyin matn yozing yoki /report ni alohida yuborib, keyingi xabarda hisobotni kiriting.")
}

func (a *App) requestAccess(c tele.Context) error { return a.start(c) }
func (a *App) reportCommand(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}
	if !a.isActiveEmployee(c.Sender().ID) {
		return c.Send("Hisobot yuborish uchun avval /request orqali kirish so‘rovini yuboring.")
	}
	payload := strings.TrimSpace(c.Message().Payload)
	if payload != "" {
		return a.saveReport(c, payload)
	}
	a.reportWait.Store(c.Sender().ID, true)
	return c.Send("Bugun bajargan ishlaringizni keyingi xabarda yozib yuboring. Bekor qilish uchun /cancel buyrug‘ini yuboring.")
}

func (a *App) cancelReport(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}
	a.reportWait.Delete(c.Sender().ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.DB.CancelRegistration(ctx, c.Sender().ID); err != nil {
		return c.Send("Bekor qilishda xatolik. Qayta urinib ko‘ring.")
	}
	if a.isAdmin(c.Sender().ID) {
		if err := a.DB.CancelAdminAction(ctx, c.Sender().ID); err != nil {
			return c.Send("Bekor qilishda xatolik.")
		}
		return c.Send("Amal bekor qilindi.", adminKeyboard())
	}
	return c.Send("Joriy kiritish bekor qilindi. Ro‘yxatdan o‘tish: /start. Hisobot: /report.", &tele.ReplyMarkup{RemoveKeyboard: true})
}

func (a *App) onText(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}
	text := strings.TrimSpace(c.Text())
	if text == "" || strings.HasPrefix(text, "/") {
		return nil
	}
	if handled, err := a.handleManagementText(c, text); handled || err != nil {
		return err
	}
	if handled, err := a.handleOnboardingText(c, text); handled || err != nil {
		return err
	}
	if _, waiting := a.reportWait.Load(c.Sender().ID); !waiting {
		return nil
	}
	if text == "/cancel" {
		return c.Send("Hisobot yuborish bekor qilindi.")
	}
	return a.saveReport(c, text)
}

func (a *App) saveReport(c tele.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return c.Send("Hisobot matni bo‘sh bo‘lmasligi kerak.")
	}
	if len([]rune(text)) > 6000 {
		return c.Send("Hisobot juda uzun. Iltimos, 6000 belgidan oshirmang.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	e, err := a.DB.GetEmployee(ctx, c.Sender().ID)
	if err != nil || !e.IsActive {
		return c.Send("Siz tizimda faol xodim sifatida topilmadingiz. /request orqali so‘rov yuboring.")
	}
	now := time.Now().In(a.Cfg.Timezone)
	if err := a.DB.SaveReport(ctx, c.Sender().ID, now, text); err != nil {
		log.Printf("save report: %v", err)
		return c.Send("Hisobotni saqlashda xatolik yuz berdi. Iltimos, qayta urinib ko‘ring.")
	}
	a.reportWait.Delete(c.Sender().ID)
	msg := fmt.Sprintf("📝 KUNLIK HISOBOT\nSana: %s\nXodim: %s\n\n%s", now.Format("02.01.2006"), e.FullName, text)
	// Per-recipient deliveries were committed atomically with SaveReport.
	if a.Cfg.ReportGroupID != 0 {
		if err := sendLongMessage(a.Bot, &tele.Chat{ID: a.Cfg.ReportGroupID}, msg); err != nil {
			log.Print("send report to group failed (details hidden)")
		}
	}
	return c.Send("✅ Hisobotingiz saqlandi. Rahbarlarga avtomatik yetkazish navbatiga qo‘yildi.")
}
func sendLongMessage(b *tele.Bot, recipient tele.Recipient, message string) error {
	for _, chunk := range splitMessage(message) {
		if _, err := b.Send(recipient, chunk); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) myReports(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}
	if !a.isActiveEmployee(c.Sender().ID) {
		return c.Send("Siz ro‘yxatdan o‘tmagansiz. /request yuboring.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	reports, err := a.DB.MyReports(ctx, c.Sender().ID, 7)
	if err != nil {
		log.Printf("my reports: %v", err)
		return c.Send("Hisobotlarni olishda xatolik.")
	}
	if len(reports) == 0 {
		return c.Send("Hozircha hisobotlaringiz yo‘q.")
	}
	var b strings.Builder
	b.WriteString("📋 Oxirgi hisobotlaringiz:\n\n")
	for i, r := range reports {
		b.WriteString(fmt.Sprintf("%d) %s\n%s\n\n", i+1, r.SubmittedAt.In(a.Cfg.Timezone).Format("02.01.2006 15:04"), truncate(r.ReportText, 900)))
	}
	return sendLong(c, b.String())
}

func (a *App) reports(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	e, err := a.DB.GetEmployee(ctx, c.Sender().ID)
	if !a.Cfg.AdminIDs[c.Sender().ID] &&
		(err != nil || !e.IsActive ||
			(e.Role != "manager" && e.Role != "advisor" && e.Role != "admin")) {
		return c.Send("Bu buyruq faqat rahbarlar va administrator uchun.")
	}

	departmentID := int64(0)
	if !a.Cfg.AdminIDs[c.Sender().ID] && e.Role == "manager" {
		if e.DepartmentID == 0 {
			return c.Send("Sizga bo‘lim biriktirilmagan. Administratorga murojaat qiling.")
		}
		departmentID = e.DepartmentID
	}

	reports, err := a.DB.ReportsForDateByDepartment(
		ctx,
		time.Now().In(a.Cfg.Timezone),
		departmentID,
	)
	if err != nil {
		log.Printf("reports: %v", err)
		return c.Send("Hisobotlarni olishda xatolik.")
	}

	if len(reports) == 0 {
		return c.Send("Bugun hali hisobot topshirilmagan.")
	}

	var b strings.Builder
	b.WriteString("📊 BUGUNGI HISOBOTLAR\n\n")

	for i, r := range reports {
		if departmentID == 0 && r.DepartmentName != "" {
			b.WriteString("🏢 Bo‘lim: " + r.DepartmentName + "\n")
		}
		b.WriteString(fmt.Sprintf(
			"%d. %s\n%s\n\n",
			i+1,
			r.FullName,
			truncate(r.ReportText, 1800),
		))
	}

	return sendLong(c, b.String())
}

func (a *App) status(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	e, err := a.DB.GetEmployee(ctx, c.Sender().ID)
	if !a.Cfg.AdminIDs[c.Sender().ID] && (err != nil || !e.IsActive || (e.Role != "manager" && e.Role != "advisor" && e.Role != "admin")) {
		return c.Send("Bu buyruq faqat rahbarlar va administrator uchun.")
	}
	departmentID := int64(0)
	if !a.Cfg.AdminIDs[c.Sender().ID] && e.Role == "manager" {
		if e.DepartmentID == 0 {
			return c.Send("Sizga bo‘lim biriktirilmagan. Administratorga murojaat qiling.")
		}
		departmentID = e.DepartmentID
	}
	now := time.Now().In(a.Cfg.Timezone)
	submitted, missing, err := a.DB.StatusEmployeesByDepartment(ctx, now, departmentID)
	if err != nil {
		log.Printf("status: %v", err)
		return c.Send("Holatni olishda xatolik.")
	}
	return sendLong(c, formatStatus(now, submitted, missing, e.DepartmentName, departmentID == 0))
}

func (a *App) whoAmI(c tele.Context) error {
	if c.Sender() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e, err := a.DB.GetEmployee(ctx, c.Sender().ID)
	if err != nil {
		return c.Send("Sizga hali rol berilmagan. /start orqali so‘rov yuboring.")
	}
	return c.Send(fmt.Sprintf("Telegram ID: %d\nUsername: @%s\nRol: %s\nBo‘lim: %s\nFaol: %t\nSana: %s", e.TelegramID, e.TelegramUsername, roleName(e.Role), e.DepartmentName, e.IsActive, time.Now().In(a.Cfg.Timezone).Format("02.01.2006")))
}

func formatStatus(now time.Time, submitted, missing []store.Employee, departmentName string, allDepartments bool) string {
	var out strings.Builder
	out.WriteString("📊 BUGUNGI HOLAT\n📅 Sana: " + now.Format("02.01.2006") + "\n")
	if allDepartments {
		out.WriteString("🏢 Barcha bo‘limlar\n")
	} else {
		out.WriteString("🏢 Bo‘lim: " + departmentName + "\n")
	}
	out.WriteString(fmt.Sprintf("Topshirgan: %d\nTopshirmagan: %d\n", len(submitted), len(missing)))
	for _, section := range []struct {
		title     string
		employees []store.Employee
	}{{"✅ Topshirganlar:", submitted}, {"⏳ Topshirmaganlar:", missing}} {
		out.WriteString("\n" + section.title + "\n")
		if len(section.employees) == 0 {
			out.WriteString("Yo‘q.\n")
		}
		for _, employee := range section.employees {
			out.WriteString("• " + employee.FullName)
			if allDepartments && employee.DepartmentName != "" {
				out.WriteString(" — " + employee.DepartmentName)
			}
			out.WriteString("\n")
		}
	}
	return out.String()
}

func (a *App) requests(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Send("Bu buyruq faqat administrator uchun.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	requests, err := a.DB.PendingRequests(ctx)
	if err != nil {
		return c.Send("So‘rovlarni olishda xatolik.")
	}
	if len(requests) == 0 {
		return c.Send("Kutilayotgan so‘rovlar yo‘q.")
	}
	for _, request := range requests {
		message, markup := requestApprovalMessage(request)
		if err := c.Send(message, markup); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) approve(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Send("Bu buyruq faqat administrator uchun.")
	}
	args := strings.Fields(c.Message().Payload)
	if len(args) < 3 {
		return c.Send("Foydalanish: /approve TELEGRAM_ID role Ism Familiya\nMasalan: /approve 123456789 employee Anvar Buriboyev")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return c.Send("Telegram ID raqam bo‘lishi kerak.")
	}
	role := strings.ToLower(args[1])
	if role == "director" || role == "direktor" {
		role = "advisor"
	}
	switch role {
	case "employee", "manager", "advisor", "admin":
	default:
		return c.Send("Role: employee, manager, advisor yoki admin bo‘lishi kerak.")
	}
	name := strings.TrimSpace(strings.Trim(strings.Join(args[2:], " "), "\""))
	if name == "" {
		return c.Send("Ism-familiya kiritilishi kerak.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := a.DB.Approve(ctx, id, name, role, c.Sender().ID); err != nil {
		log.Printf("approve: %v", err)
		return c.Send("Foydalanuvchini tasdiqlashda xatolik yuz berdi.")
	}
	if err := a.queueLeaderOverview(ctx, id); err != nil {
		log.Print("queue approved leader overview failed (details hidden)")
	}
	_, _ = a.Bot.Send(&tele.User{ID: id}, fmt.Sprintf("✅ UNICON hisobot botiga kirishingiz tasdiqlandi.\nIsm: %s\nRol: %s\nHisobot: /report", name, role))
	return c.Send(fmt.Sprintf("✅ %s (%d) foydalanuvchisi %s roli bilan tasdiqlandi.", name, id, role))
}

func (a *App) isActiveEmployee(id int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e, err := a.DB.GetEmployee(ctx, id)
	return err == nil && e.IsActive
}

func (a *App) isAdmin(id int64) bool {
	if a.Cfg.AdminIDs[id] {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e, err := a.DB.GetEmployee(ctx, id)
	return err == nil && e.IsActive && e.Role == "admin"
}

func (a *App) canViewReports(id int64) bool {
	if a.Cfg.AdminIDs[id] {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e, err := a.DB.GetEmployee(ctx, id)
	return err == nil && e.IsActive && (e.Role == "manager" || e.Role == "advisor" || e.Role == "admin")
}

func sendLong(c tele.Context, text string) error {
	for _, chunk := range splitMessage(text) {
		if err := c.Send(chunk); err != nil {
			return err
		}
	}
	return nil
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (a *App) setDepartment(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Send("Bu buyruq faqat administrator uchun.")
	}

	args := strings.Fields(c.Message().Payload)
	if len(args) != 2 {
		return c.Send("Foydalanish: /setdept TELEGRAM_ID DEPARTMENT_ID")
	}

	telegramID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || telegramID <= 0 {
		return c.Send("Telegram ID noto‘g‘ri.")
	}

	departmentID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || departmentID <= 0 {
		return c.Send("Bo‘lim ID noto‘g‘ri.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	err = a.DB.AssignDepartment(ctx, telegramID, departmentID)
	if err != nil {
		log.Printf("assign department: %v", err)
		return c.Send("Xodimni bo‘limga biriktirib bo‘lmadi. ID larni tekshiring.")
	}
	if err := a.queueLeaderOverview(ctx, telegramID); err != nil {
		log.Print("queue department overview failed (details hidden)")
	}

	return c.Send(fmt.Sprintf(
		"✅ Telegram ID %d xodimi %d-bo‘limga biriktirildi.",
		telegramID,
		departmentID,
	))
}

func (a *App) departments(c tele.Context) error {
	if c.Sender() == nil || !a.isAdmin(c.Sender().ID) {
		return c.Send("Bu buyruq faqat administrator uchun.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	departments, err := a.DB.Departments(ctx)
	if err != nil {
		log.Printf("departments: %v", err)
		return c.Send("Bo‘limlar ro‘yxatini olishda xatolik.")
	}

	if len(departments) == 0 {
		return c.Send("Hozircha bo‘limlar mavjud emas.")
	}

	var b strings.Builder
	b.WriteString("🏢 BO‘LIMLAR RO‘YXATI\n\n")

	for _, d := range departments {
		b.WriteString(fmt.Sprintf("ID: %d — %s\n", d.ID, d.Name))
	}

	return sendLong(c, b.String())
}
