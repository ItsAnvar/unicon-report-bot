# UNICON Report Bot

Telegram orqali kunlik hisobotlar, bo‘limlar va foydalanuvchi rollarini boshqarish. Go 1.23+, PostgreSQL 16. Standart vaqt zonasi: Asia/Tashkent.

## Vazifalar

- Ism-familiya → bo‘lim yoki **Bosh direktor maslahatchisi** → bo‘lim tanlansa Xodim yoki Bo‘lim boshlig‘i → admin tasdig‘i.
- Bosh direktor maslahatchisi barcha bo‘limlar hisobotlarini oladi; bo‘lim boshlig‘i faqat o‘z bo‘limini ko‘radi.
- `/admin`: bo‘lim qo‘shish, nomini o‘zgartirish, arxivlash, rollarni boshqarish va kirishni bekor qilish.
- `/report`, `/myreports`, `/reports`, `/status`, `/requests`, `/whoami`, `/version`.
- Xabarlarni saqlanadigan navbat orqali yuborish va qayta urinish.
- Kunlik yakun 19:00 da; vaqtlar `.env` orqali sozlanadi.
- `007` migratsiyasi dastlabki `IUTXRvaEQB` nomini **Eri KORM** ga almashtiradi. Keyingi bo‘limlar admin menyusidan boshqariladi.

## VPS ga o‘rnatish (Ubuntu/Debian, systemd)

Oldindan Git, Go 1.23 yoki yangiroq, Docker Engine va Docker Compose plugin o‘rnatilgan bo‘lsin. Buyruqlar `sudo` huquqi bor foydalanuvchi orqali bajariladi.

```bash
sudo git clone https://github.com/ItsAnvar/unicon-report-bot.git /opt/unicon-report-bot
sudo chown -R "$USER":"$USER" /opt/unicon-report-bot
cd /opt/unicon-report-bot
cp .env.example .env
cp docker-compose.example.yml docker-compose.yml
nano .env
```

`.env` ichida `BOT_TOKEN`, `ADMIN_TELEGRAM_IDS`, `POSTGRES_PASSWORD` va `DATABASE_URL` ni o‘zingiz kiriting. `DATABASE_URL` paroli `POSTGRES_PASSWORD` bilan bir xil bo‘lsin. Maxsus belgilar URL formatida kodlanadi; `openssl rand -hex 24` bilan olingan parol bu muammoni oldini oladi. Compose misolidagi baza `unicon_reports`, foydalanuvchi `unicon_bot`, ulanish `127.0.0.1:5432` orqali. Token va parollarni GitHub yoki chatga yubormang.

```bash
chmod 600 .env
# PostgreSQL konteynerini ishga tushiring va tayyorligini tekshiring.
docker compose up -d postgres
docker compose exec -T postgres pg_isready -U unicon_bot -d unicon_reports
```

`pg_isready` server tayyorligini bildirgandan so‘ng, FAQAT YANGI bo‘sh bazada dastlabki sxemani yarating:

```bash
for file in migrations/001_init.sql migrations/002_departments.sql migrations/003_department_names.sql; do
  docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U unicon_bot -d unicon_reports < "$file" || exit 1
done
go run ./cmd/migrate
go run ./cmd/diagnose -skip-telegram
mkdir -p bin
go build -o bin/unicon-report-bot ./cmd/bot
```

Mavjud bazada `001–003` ni qayta ishlatmang. `cmd/migrate` keyingi `004–007` migratsiyalarni jurnalda qayd etib, bir marta qo‘llaydi. Mavjud `IUTXRvaEQB` va `Eri KORM` ikkalasi ham mavjud bo‘lsa, `007` avtomatik birlashtirmaydi: avval mavjud bo‘limlarni tekshiring. Yangilashdan oldin doim bazadan zaxira oling.

Botni doimiy xizmat sifatida ishga tushirish:

```bash
sudo useradd --system --home-dir /opt/unicon-report-bot --shell /usr/sbin/nologin uniconbot
# Agar foydalanuvchi oldin yaratilgan bo‘lsa, useradd bosqichini takrorlamang.
sudo chown "$USER":uniconbot .env
sudo chmod 640 .env
sudo cp deploy/unicon-report-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now unicon-report-bot
sudo systemctl status unicon-report-bot --no-pager
```

Loglar: `sudo journalctl -u unicon-report-bot -n 50 --no-pager`. Telegramda asosiy admin akkauntidan `/start` yuboring. Bir token bilan bir vaqtning o‘zida faqat bitta bot ishlasin; VPS ishlayotganda lokal nusxani shu token bilan yoqmang.

## VPS ni yangilash

Loyiha papkasida `.env` va `docker-compose.yml` ni saqlang; ular Git orqali kuzatilmaydi.

```bash
cd /opt/unicon-report-bot
mkdir -p backups
chmod 700 backups
# Zaxira faylni hech qachon ommaga yuklamang.
docker compose exec -T postgres pg_dump -U unicon_bot -d unicon_reports -Fc > "backups/before-update-$(date +%Y%m%d-%H%M%S).dump" || exit 1
sudo systemctl stop unicon-report-bot
git pull --ff-only
go run ./cmd/migrate && go run ./cmd/diagnose -skip-telegram || exit 1
go build -o bin/unicon-report-bot ./cmd/bot || exit 1
sudo systemctl start unicon-report-bot
sudo systemctl status unicon-report-bot --no-pager
```

Docker volume mavjud ma’lumotlarni saqlaydi. `docker compose down -v` bazani o‘chiradi; yangilash uchun ishlatmang. PostgreSQL porti misolda faqat `127.0.0.1` ga bog‘langan.

## Tekshiruv

```bash
go test ./...
go vet ./...
# Alohida vaqtinchalik PostgreSQL sxemalarida integratsion sinovlar:
UNICON_DB_TEST=1 go test ./... -count=1
```

Integratsion sinovlar uchun `.env` dagi PostgreSQL foydalanuvchisida sxema yaratish huquqi bo‘lsin. Telegram sinovlari xotiradagi transportdan foydalanadi, haqiqiy xabar yubormaydi.

Ochiq repository tarkibida faqat kod, migratsiyalar va sozlama namunalari bor. Ishchi `.env`, tokenlar, foydalanuvchi ma’lumotlari, baza zaxiralari va tayyor lokal binary fayllar yuklanmaydi.
