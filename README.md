# Telegram Reminder Bot

A Telegram bot written in Go that lets users schedule:

- One-time reminders via an interactive calendar/clock UI  
- Recurring reminders with full Cron-expression support  
- Multi-language interface (English & 中文)  
- Persistent storage in a JSON file  

Built with  
- Go modules  
- [go-telegram-bot-api/v5](https://github.com/go-telegram-bot-api/telegram-bot-api)  
- [gorhill/cronexpr](https://github.com/gorhill/cronexpr)  

---

## 📦 Features

- **Interactive setup**  
  • Calendar UI for picking dates  
  • Clock UI for picking times (10-minute steps, AM/PM)  
  • Time-zone selector (UTC offset)  
  • Optional extra information  

- **One-time reminders**  
  • Fires 10 minutes before the scheduled time  
  • Auto-deletes after firing  

- **Recurring reminders**  
  • Uses `cronexpr.Parse()` to validate syntax & ranges  
  • Full Cron syntax: `*` / lists / ranges / steps / L/W/# etc.  
  • Time-zone aware (per-job TZ)  
  • Each job runs in its own goroutine, using `expr.Next()`  

- **Multi-language (i18n)**  
  • English (default) and Chinese support  
  • `/language` command to switch  

- **Persistent storage**  
  • All reminders + user settings in `reminder.json`  
  • On restart, automatically resumes pending jobs  

---

## ⚙️ Requirements

- Go 1.18+  
- A Telegram Bot Token (get one from [@BotFather](https://t.me/BotFather))  

---

## 🚀 Installation & Run

1. Clone the repo  
   ```bash
   git clone https://github.com/2274268506/Telegram-Reminder-Bot-go.git
   cd Telegram-Reminder-Bot-go
   ```

2. Initialize modules & download deps  
   ```bash
   go mod tidy
   ```

3.  In the config.json file, enter your Telegram Bot Token
   ```jsonc
   {
     "token": "YOUR_TELEGRAM_BOT_TOKEN"
   }
   ```

4. Build & run  
   ```bash
   go run main.go
   ```

   or

   ```bash
   go build -o reminder-bot
   ./reminder-bot
   ```

---

## 🤖 Bot Commands

### /start  
Begin one-time reminder setup (name → date → time → extra).

### /cancel [index]  
- `/cancel`  
  Lists your pending reminders with inline buttons to cancel.  
- `/cancel 2`  
  Cancel the 2nd reminder directly.

### /list  
Show all your pending reminders (one-time & cron).

### /time  
Set your default UTC offset (used for one-time reminders).

### /language or /lang  
Switch interface language (English ⇄ 中文).

### /cron `<min> <hour> <dom> <mon> <dow> <TZ> <text>`  
Schedule a recurring Cron-style reminder.

- `<min> <hour> <day-of-month> <month> <day-of-week>`  
- `<TZ>`: IANA timezone name, e.g. `Asia/Shanghai`  
- `<text>`: Reminder message  

Example:  
```
/cron 0 11 1 * * Asia/Shanghai 月报提醒
```

On success you’ll see:  
```
✅ Cron reminder set: `0 11 1 * *` ⇒ 月报提醒
```

---

## 🗄️ Storage

All user settings and reminders are stored in `reminder.json`. Structure:

```jsonc
{
  "reminder": {
    "<chatID>": {
      "utc": 8,
      "lang": "zh",
      "reminder": [
        {
          "id": 123456,
          "name": "Team Sync",
          "date": "15/11/2025",
          "time": "3:00 PM",
          "opt_inf": "Zoom link…"
        },
        {
          "id": 234567,
          "name": "Monthly Report Reminder",
          "cron_original": "0 11 1 * *",
          "tz": "Asia/Shanghai",
          "cron_expr": "0 11 1 * *"
        }
      ]
    }
  }
}
```

- One-time reminders use `date`+`time`; recurring use `cron_original`+`tz`+`cron_expr`.

---

## 🔧 How It Works

1. **Interactive Flow**  
   User sends `/start` → bot asks for name → calendar → clock → extra info → save.

2. **One-time Scheduling**  
   - Parses `Date` & `Time` + user’s UTC offset → compute UTC event time  
   - Schedules a `time.AfterFunc` at **event minus 10 minutes** → sends notification → auto-deletes.

3. **Cron Scheduling**  
   - User’s 5-field cron spec is validated by `cronexpr.Parse()`  
   - Each cron job spawns a goroutine running `for { next = expr.Next(now); sleep until next; notify }`  
   - Cancel by closing a `quit` channel mapped by job ID.

4. **Persistence & Resume**  
   On startup, the bot loads `reminder.json` and:
   - Re-schedules all pending one-time `time.AfterFunc`’s  
   - Restarts all cron goroutines  

5. **Concurrency**  
   - A `sync.Mutex` protects access to the JSON store.  
   - Cron jobs each live in their own goroutine, gracefully stopped on cancel.

---
## docker

```
docker build -t reminder-bot:latest .
```

```
docker run -d \
  --restart unless-stopped \
  --name reminder-bot \
  -v /root/docker/reminder-bot/config.json:/root/config.json \
  -v /root/docker/reminder-bot/reminder.json:/root/reminder.json \
  reminder-bot:latest
```

## 🔗 References


https://github.com/dome272/Telegram-Reminder-Bot


