package main

import (
  "encoding/json"
  "fmt"
  "io/ioutil"
  "log"
  "os"
  "strconv"
  "strings"
  "sync"
  "time"

  "github.com/gorhill/cronexpr"
  tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// --------- 配置 ---------
type Config struct {
  Token string `json:"token"`
}

func loadConfig(path string) (*Config, error) {
  bs, err := ioutil.ReadFile(path)
  if err != nil {
    return nil, err
  }
  var cfg Config
  if err := json.Unmarshal(bs, &cfg); err != nil {
    return nil, err
  }
  if cfg.Token == "" {
    return nil, fmt.Errorf("token 为空，请检查 %s", path)
  }
  return &cfg, nil
}

// --------- 存储 ---------
type Reminder struct {
  Name          string `json:"name"`
  Date          string `json:"date"`               // 一次性提醒用
  Time          string `json:"time"`               // 一次性提醒用
  ID            int    `json:"id"`
  OptInfo       string `json:"opt_inf"`
  CronOriginal  string `json:"cron_original,omitempty"` // 用户原始表达式
  TZ            string `json:"tz,omitempty"`
  CronExpr      string `json:"cron_expr,omitempty"`
}

type UserData struct {
  UTC       int        `json:"utc"`
  Reminders []Reminder `json:"reminder"`
  Lang      string     `json:"lang"`
}

type Storage struct {
  Reminder map[string]*UserData `json:"reminder"`
  mu       sync.Mutex           `json:"-"`
}

var (
  store       = Storage{}
  bot         *tgbotapi.BotAPI
  sessions    = make(map[int64]*Session)
  sessMu      sync.Mutex
  cronQuitMap = make(map[int]chan struct{}) // 用于取消 cronexpr 调度
)

// load/save
func loadStorage() error {
  store.mu.Lock()
  defer store.mu.Unlock()
  if _, err := os.Stat("reminder.json"); os.IsNotExist(err) {
    store.Reminder = make(map[string]*UserData)
    return saveStorage()
  }
  bs, err := ioutil.ReadFile("reminder.json")
  if err != nil {
    return err
  }
  if err := json.Unmarshal(bs, &store); err != nil {
    return err
  }
  if store.Reminder == nil {
    store.Reminder = make(map[string]*UserData)
  }
  return nil
}

func saveStorage() error {
  store.mu.Lock()
  defer store.mu.Unlock()
  bs, err := json.MarshalIndent(store, "", "  ")
  if err != nil {
    return err
  }
  return ioutil.WriteFile("reminder.json", bs, 0644)
}

func getUserData(chatID int64) *UserData {
  key := strconv.FormatInt(chatID, 10)
  store.mu.Lock()
  defer store.mu.Unlock()
  ud, ok := store.Reminder[key]
  if !ok {
    ud = &UserData{UTC: 0, Reminders: []Reminder{}, Lang: "en"}
    store.Reminder[key] = ud
  }
  if ud.Lang != "en" && ud.Lang != "zh" {
    ud.Lang = "en"
  }
  return ud
}

// --------- 删除提醒 ---------
func deleteReminder(chatID int64, rid int, head bool) {
  ud := getUserData(chatID)
  if head {
    if len(ud.Reminders) > 0 {
      r := ud.Reminders[0]
      if r.CronExpr != "" {
        if quit, ok := cronQuitMap[r.ID]; ok {
          close(quit)
          delete(cronQuitMap, r.ID)
        }
      }
      ud.Reminders = ud.Reminders[1:]
    }
  } else {
    for i, r := range ud.Reminders {
      if r.ID == rid {
        if r.CronExpr != "" {
          if quit, ok := cronQuitMap[r.ID]; ok {
            close(quit)
            delete(cronQuitMap, r.ID)
          }
        }
        ud.Reminders = append(ud.Reminders[:i], ud.Reminders[i+1:]...)
        break
      }
    }
  }
  saveStorage()
}

func deleteByIndex(chatID int64, idx int) bool {
  ud := getUserData(chatID)
  if idx < 1 || idx > len(ud.Reminders) {
    return false
  }
  r := ud.Reminders[idx-1]
  if r.CronExpr != "" {
    if quit, ok := cronQuitMap[r.ID]; ok {
      close(quit)
      delete(cronQuitMap, r.ID)
    }
  }
  ud.Reminders = append(ud.Reminders[:idx-1], ud.Reminders[idx:]...)
  saveStorage()
  return true
}

// --------- 文本多语言 ---------
var messages = map[string]map[string]string{
  "prompt_name":     {"en": "📍 *Reminder Setup*\n\nWhat is the name of your appointment?", "zh": "📍 *提醒设置*\n\n请输入您的日程名称："},
  "prompt_date":     {"en": "Select a date:", "zh": "请选择日期："},
  "prompt_time":     {"en": "You selected %s\n\nChoose time:", "zh": "您选择了 %s\n\n请选择时间："},
  "ask_extra":       {"en": "You selected %s\nAdd extra information?", "zh": "您选择了 %s\n是否需要添加更多信息？"},
  "prompt_optinfo":  {"en": "Please send additional information:", "zh": "请输入附加信息："},
  "no_extra":        {"en": "No extra info. Saving…", "zh": "不添加附加信息，正在保存…"},
  "saved":           {"en": "📌 *Saved*\n\nAppointment: %s\nDate: %s\nTime: %s", "zh": "📌 *已保存*\n\n日程：%s\n日期：%s\n时间：%s"},
  "list_empty":      {"en": "📋 You have no reminders.", "zh": "📋 您还没有任何提醒。"},
  "list_header":     {"en": "📋 *Reminder List*\n", "zh": "📋 *日程列表*\n"},
  "timezone_prompt": {"en": "Choose your UTC offset:", "zh": "请选择您的 UTC 时区偏移："},
  "timezone_set":    {"en": "Your UTC offset is now %+d", "zh": "您的 UTC 偏移已设置为 %+d"},
  "cancelled":       {"en": "🚫 Reminder Setup canceled.", "zh": "🚫 已取消提醒设置。"},
  "cancelled_index": {"en": "🚫 Cancelled reminder #%d.", "zh": "🚫 已取消第 %d 条提醒。"},
  "invalid_index":   {"en": "❌ Invalid index", "zh": "❌ 无效的序号"},
  "notify":          {"en": "💡 *Reminder*\n\nAppointment: %s\nScheduled for %s - %s.\nThe appointment starts in 10 minutes!", "zh": "💡 *提醒*\n\n日程：%s\n安排在 %s - %s。\n距离开始还有 10 分钟！"},
  "notify_cron":     {"en": "⏰ *Cron Reminder*\n\n%s", "zh": "⏰ *定时提醒*\n\n%s"},
  "lang_prompt":     {"en": "Please choose language / 请选择语言：", "zh": "请切换语言 / Please choose language："},
  "lang_set_en":     {"en": "Language set to English.", "zh": "Language set to English."},
  "lang_set_zh":     {"en": "语言已切换至中文。", "zh": "语言已切换至中文。"},
  "btn_yes":         {"en": "Yes", "zh": "是"},
  "btn_no":          {"en": "No", "zh": "否"},
  "btn_en":          {"en": "English", "zh": "English"},
  "btn_zh":          {"en": "中文", "zh": "中文"},
  "cron_usage": {
    "en": "Usage: /cron <min> <hour> <day> <month> <dow> <TZ> <text>\nExample: `/cron 0 11 18 * * Asia/Shanghai Monthly report`",
    "zh": "用法: /cron <分> <时> <日> <月> <周> <时区> <内容>\n例如: `/cron 0 11 18 * * Asia/Shanghai 月报提醒`",
  },
  "cron_set":    {"en": "✅ Cron reminder set: `%s` ⇒ %s", "zh": "✅ 已设置定时提醒：`%s` ⇒ %s"},
  "cancel_prompt": {"en": "❓ Select which reminder to cancel:", "zh": "❓ 请选择要取消的提醒："},
}

func sendText(chatID int64, key string, a ...interface{}) {
  ud := getUserData(chatID)
  text := fmt.Sprintf(messages[key][ud.Lang], a...)
  msg := tgbotapi.NewMessage(chatID, text)
  msg.ParseMode = "Markdown"
  bot.Send(msg)
}

func editText(chatID int64, msgID int, key string, a ...interface{}) tgbotapi.EditMessageTextConfig {
  ud := getUserData(chatID)
  text := fmt.Sprintf(messages[key][ud.Lang], a...)
  edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
  edit.ParseMode = "Markdown"
  return edit
}

// --------- 会话 ---------
type Stage int

const (
  StageIdle Stage = iota
  StageName
  StageDate
  StageTime
  StageAskInfo
  StageOptInfo
  StageUTC
)

type Session struct {
  Stage  Stage
  Temp   Reminder
  ChatID int64
}

func getSession(chatID int64) *Session {
  sessMu.Lock()
  defer sessMu.Unlock()
  s, ok := sessions[chatID]
  if !ok {
    s = &Session{Stage: StageIdle, ChatID: chatID}
    sessions[chatID] = s
  }
  return s
}

func finalizeReminder(s *Session) {
  chatID := s.ChatID
  ud := getUserData(chatID)
  s.Temp.ID = int(time.Now().UnixNano() % 1e6)
  ud.Reminders = append([]Reminder{s.Temp}, ud.Reminders...)
  saveStorage()
  scheduleOnce(chatID, s.Temp)
  sendText(chatID, "saved", s.Temp.Name, s.Temp.Date, s.Temp.Time)
  s.Stage = StageIdle
  s.Temp = Reminder{}
}

// --------- 一次性 调度 ---------
func scheduleOnce(chatID int64, r Reminder) {
  ud := getUserData(chatID)
  dparts := strings.Split(r.Date, "/")
  day, _ := strconv.Atoi(dparts[0])
  mon, _ := strconv.Atoi(dparts[1])
  yr, _ := strconv.Atoi(dparts[2])
  tparts := strings.Split(r.Time, " ")
  hm := strings.Split(tparts[0], ":")
  hh, _ := strconv.Atoi(hm[0])
  mi, _ := strconv.Atoi(hm[1])
  ap := strings.ToLower(tparts[1])
  if ap == "pm" && hh < 12 {
    hh += 12
  }
  if ap == "am" && hh == 12 {
    hh = 0
  }
  evtUTC := time.Date(yr, time.Month(mon), day, hh, mi, 0, 0, time.UTC).
    Add(-time.Duration(ud.UTC) * time.Hour)
  notifyUTC := evtUTC.Add(-10 * time.Minute)
  nowUTC := time.Now().UTC()
  delay := notifyUTC.Sub(nowUTC)
  if delay <= 0 {
    delay = time.Second
  }
  log.Printf("[Reminder %d] at %v (in %v)\n", r.ID, notifyUTC, delay)
  time.AfterFunc(delay, func() {
    sendText(chatID, "notify", r.Name, r.Date, r.Time)
    deleteReminder(chatID, r.ID, false)
  })
}

// --------- Cron 调度 （cronexpr） ---------
func runExprJob(chatID int64, r Reminder, expr *cronexpr.Expression, loc *time.Location, quit chan struct{}) {
  for {
    now := time.Now().In(loc)
    next := expr.Next(now)
    wait := time.Until(next)
    if wait <= 0 {
      wait = time.Second
    }
    select {
    case <-time.After(wait):
      sendText(chatID, "notify_cron", r.Name)
    case <-quit:
      return
    }
  }
}

// --------- 消息 处理 ---------
func handleMessage(msg *tgbotapi.Message) {
  chatID := msg.Chat.ID
  ud := getUserData(chatID)
  s := getSession(chatID)

  if msg.IsCommand() {
    switch msg.Command() {
    case "start":
      s.Stage = StageName
      sendText(chatID, "prompt_name")
      return

    case "cancel":
      args := msg.CommandArguments()
      if args != "" {
        idx, err := strconv.Atoi(args)
        if err != nil || !deleteByIndex(chatID, idx) {
          sendText(chatID, "invalid_index")
        } else {
          sendText(chatID, "cancelled_index", idx)
        }
        s.Stage = StageIdle
        return
      }
      if len(ud.Reminders) == 0 {
        sendText(chatID, "list_empty")
        return
      }
      var rows [][]tgbotapi.InlineKeyboardButton
      for i, r := range ud.Reminders {
        text := fmt.Sprintf("%d) %s", i+1, r.Name)
        data := fmt.Sprintf("CANCELIDX;%d", i+1)
        btn := tgbotapi.NewInlineKeyboardButtonData(text, data)
        rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
      }
      kb := tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
      m := tgbotapi.NewMessage(chatID, messages["cancel_prompt"][ud.Lang])
      m.ReplyMarkup = kb
      bot.Send(m)
      return

    case "list":
      if len(ud.Reminders) == 0 {
        sendText(chatID, "list_empty")
        return
      }
      text := messages["list_header"][ud.Lang] + "\n"
      for idx, r := range ud.Reminders {
        line := fmt.Sprintf("%d) %s", idx+1, r.Name)
        if r.CronExpr != "" {
          line += fmt.Sprintf("   (cron: `%s` TZ:%s)", r.CronOriginal, r.TZ)
        } else {
          line += fmt.Sprintf("   %s %s", r.Date, r.Time)
        }
        if r.OptInfo != "" {
          line += "\n   Info: " + r.OptInfo
        }
        text += "\n" + line
      }
      m := tgbotapi.NewMessage(chatID, text)
      m.ParseMode = "Markdown"
      bot.Send(m)
      return

    case "time":
      s.Stage = StageUTC
      m := tgbotapi.NewMessage(chatID, messages["timezone_prompt"][ud.Lang])
      m.ReplyMarkup = CreateTimezone(ud.UTC)
      bot.Send(m)
      return

    case "language", "lang":
      kb := tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
          tgbotapi.NewInlineKeyboardButtonData(messages["btn_en"][ud.Lang], "LANG;en"),
          tgbotapi.NewInlineKeyboardButtonData(messages["btn_zh"][ud.Lang], "LANG;zh"),
        ),
      )
      m := tgbotapi.NewMessage(chatID, messages["lang_prompt"][ud.Lang])
      m.ReplyMarkup = kb
      bot.Send(m)
      return

	case "cron":
		fields := strings.Fields(msg.CommandArguments())
		if len(fields) < 7 {
			sendText(chatID, "cron_usage")
			return
		}

		spec := strings.Join(fields[0:5], " ")
		tzName := fields[5]
		text := strings.Join(fields[6:], " ")

		// 1) 加载时区
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			// 方案 A：直接发原始文本
			msg := tgbotapi.NewMessage(chatID,
				fmt.Sprintf("❌ 无效时区：%s", tzName))
			msg.ParseMode = "Markdown"
			bot.Send(msg)
			return
			// 方案 B：走 sendText，需要在 messages 里添加 err_invalid_tz key
			// sendText(chatID, "err_invalid_tz", tzName)
			// return
		}

		// 2) 语法+范围校验
		expr, err := cronexpr.Parse(spec)
		if err != nil {
			msg := tgbotapi.NewMessage(chatID,
				fmt.Sprintf("❌ Cron 表达式解析失败：%s", err.Error()))
			msg.ParseMode = "Markdown"
			bot.Send(msg)
			return
		}
      // 存储
      r := Reminder{
        ID:           int(time.Now().UnixNano() % 1e6),
        Name:         text,
        CronOriginal: spec,
        TZ:           tzName,
        CronExpr:     spec,
      }
      ud.Reminders = append(ud.Reminders, r)
      saveStorage()
      // 启动
      quit := make(chan struct{})
      cronQuitMap[r.ID] = quit
      go runExprJob(chatID, r, expr, loc, quit)
      sendText(chatID, "cron_set", spec, text)
      return
    }
  }

  // 会话流程：一次性提醒
  switch s.Stage {
  case StageName:
    s.Temp.Name = msg.Text
    s.Stage = StageDate
    kb := CreateCalendar(time.Now().Year(), int(time.Now().Month()))
    m := tgbotapi.NewMessage(chatID, messages["prompt_date"][ud.Lang])
    m.ReplyMarkup = kb
    bot.Send(m)
  case StageOptInfo:
    s.Temp.OptInfo = msg.Text
    finalizeReminder(s)
  case StageAskInfo:
    lower := strings.ToLower(msg.Text)
    yes := messages["btn_yes"][ud.Lang]
    if lower == strings.ToLower(yes) {
      s.Stage = StageOptInfo
      sendText(chatID, "prompt_optinfo")
    } else {
      finalizeReminder(s)
    }
  }
}

// --------- Callback 处理 ---------
func handleCallback(q *tgbotapi.CallbackQuery) {
  chatID := q.Message.Chat.ID
  ud := getUserData(chatID)
  s := getSession(chatID)
  data := q.Data

  if strings.HasPrefix(data, "CANCELIDX;") {
    parts := strings.Split(data, ";")
    idx, _ := strconv.Atoi(parts[1])
    if deleteByIndex(chatID, idx) {
      bot.Request(tgbotapi.NewEditMessageReplyMarkup(chatID, q.Message.MessageID, tgbotapi.InlineKeyboardMarkup{}))
      sendText(chatID, "cancelled_index", idx)
    } else {
      sendText(chatID, "invalid_index")
    }
    bot.Request(tgbotapi.NewCallback(q.ID, ""))
    return
  }

  if strings.HasPrefix(data, "LANG;") {
    parts := strings.Split(data, ";")
    ud.Lang = parts[1]
    saveStorage()
    if ud.Lang == "en" {
      sendText(chatID, "lang_set_en")
    } else {
      sendText(chatID, "lang_set_zh")
    }
    bot.Request(tgbotapi.NewEditMessageReplyMarkup(chatID, q.Message.MessageID, tgbotapi.InlineKeyboardMarkup{}))
    return
  }

  // 日期选择
  if s.Stage == StageDate {
    ok, y, m, d := ProcessCalendar(q)
    if ok {
      s.Temp.Date = fmt.Sprintf("%02d/%02d/%04d", d, m, y)
      s.Stage = StageTime
      kb := CreateClock(12, 0, "am")
      edit := editText(chatID, q.Message.MessageID, "prompt_time", s.Temp.Date)
      edit.ReplyMarkup = &kb
      bot.Send(edit)
    }
    return
  }

  // 时间选择
  if s.Stage == StageTime {
    ok, h, mi, ap := ProcessClock(q)
    if ok {
      s.Temp.Time = fmt.Sprintf("%d:%02d %s", h, mi, ap)
      s.Stage = StageAskInfo
      kb := tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
          tgbotapi.NewInlineKeyboardButtonData(messages["btn_yes"][ud.Lang], "askinfo_yes"),
          tgbotapi.NewInlineKeyboardButtonData(messages["btn_no"][ud.Lang], "askinfo_no"),
        ),
      )
      edit := tgbotapi.NewEditMessageText(chatID, q.Message.MessageID,
        fmt.Sprintf(messages["ask_extra"][ud.Lang], s.Temp.Time))
      edit.ParseMode = "Markdown"
      edit.ReplyMarkup = &kb
      bot.Send(edit)
    }
    return
  }

  // askinfo
  if s.Stage == StageAskInfo && (data == "askinfo_yes" || data == "askinfo_no") {
    if data == "askinfo_yes" {
      s.Stage = StageOptInfo
      bot.Send(tgbotapi.NewEditMessageText(chatID, q.Message.MessageID, messages["prompt_optinfo"][ud.Lang]))
    } else {
      bot.Send(tgbotapi.NewEditMessageText(chatID, q.Message.MessageID, messages["no_extra"][ud.Lang]))
      finalizeReminder(s)
    }
    return
  }

  // UTC offset
  if s.Stage == StageUTC {
    done, off := ProcessUTC(q)
    if done {
      ud.UTC = off
      saveStorage()
      bot.Send(tgbotapi.NewEditMessageText(chatID, q.Message.MessageID,
        fmt.Sprintf(messages["timezone_set"][ud.Lang], off)))
      s.Stage = StageIdle
    }
    return
  }

  bot.Request(tgbotapi.NewCallback(q.ID, ""))
}

// --------- 日历 ---------
func CreateCalendar(year, month int) tgbotapi.InlineKeyboardMarkup {
  var rows [][]tgbotapi.InlineKeyboardButton
  rows = append(rows, tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%s %d", time.Month(month), year), "ignore"),
  ))
  weekDays := []string{"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"}
  var hdr []tgbotapi.InlineKeyboardButton
  for _, d := range weekDays {
    hdr = append(hdr, tgbotapi.NewInlineKeyboardButtonData(d, "ignore"))
  }
  rows = append(rows, hdr)
  weeks := monthCalendar(year, month)
  for _, wk := range weeks {
    var row []tgbotapi.InlineKeyboardButton
    for _, d := range wk {
      if d == 0 {
        row = append(row, tgbotapi.NewInlineKeyboardButtonData(" ", "ignore"))
      } else {
        data := fmt.Sprintf("DAY;%d;%d;%d", year, month, d)
        row = append(row, tgbotapi.NewInlineKeyboardButtonData(strconv.Itoa(d), data))
      }
    }
    rows = append(rows, row)
  }
  prev := fmt.Sprintf("PREV;%d;%d;0", year, month)
  next := fmt.Sprintf("NEXT;%d;%d;0", year, month)
  rows = append(rows, tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData("<", prev),
    tgbotapi.NewInlineKeyboardButtonData(" ", "ignore"),
    tgbotapi.NewInlineKeyboardButtonData(">", next),
  ))
  return tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func monthCalendar(year, month int) [][]int {
  first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
  start := (int(first.Weekday()) + 6) % 7
  days := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.Local).Day()
  var weeks [][]int
  week := make([]int, 7)
  for i := 0; i < start; i++ {
    week[i] = 0
  }
  d := 1
  for d <= days {
    idx := (start + d - 1) % 7
    week[idx] = d
    if idx == 6 {
      weeks = append(weeks, week)
      week = make([]int, 7)
    }
    d++
  }
  if d > days {
    weeks = append(weeks, week)
  }
  return weeks
}

func ProcessCalendar(q *tgbotapi.CallbackQuery) (bool, int, int, int) {
  parts := strings.Split(q.Data, ";")
  act := parts[0]
  y, _ := strconv.Atoi(parts[1])
  m, _ := strconv.Atoi(parts[2])
  d, _ := strconv.Atoi(parts[3])
  switch act {
  case "ignore":
    bot.Request(tgbotapi.NewCallback(q.ID, ""))
  case "DAY":
    bot.Request(tgbotapi.NewCallback(q.ID, ""))
    return true, y, m, d
  case "PREV":
    prev := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.Local).AddDate(0, -1, 0)
    bot.Request(tgbotapi.NewEditMessageReplyMarkup(q.Message.Chat.ID, q.Message.MessageID,
      CreateCalendar(prev.Year(), int(prev.Month()))))
  case "NEXT":
    nxt := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.Local).AddDate(0, +1, 0)
    bot.Request(tgbotapi.NewEditMessageReplyMarkup(q.Message.Chat.ID, q.Message.MessageID,
      CreateCalendar(nxt.Year(), int(nxt.Month()))))
  }
  return false, 0, 0, 0
}

// --------- 时钟 ---------
func CreateClock(hour, minute int, ampm string) tgbotapi.InlineKeyboardMarkup {
  r1 := tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData("↑h", fmt.Sprintf("PLUS-HOUR;%d;%d;%s", hour, minute, ampm)),
    tgbotapi.NewInlineKeyboardButtonData("↑m", fmt.Sprintf("PLUS-MINUTE;%d;%d;%s", hour, minute, ampm)),
    tgbotapi.NewInlineKeyboardButtonData("±", fmt.Sprintf("PLUS-AMPM;%d;%d;%s", hour, minute, ampm)),
  )
  r2 := tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%2d", hour), "ignore"),
    tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%02d", minute), "ignore"),
    tgbotapi.NewInlineKeyboardButtonData(ampm, "ignore"),
  )
  r3 := tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData("↓h", fmt.Sprintf("MINUS-HOUR;%d;%d;%s", hour, minute, ampm)),
    tgbotapi.NewInlineKeyboardButtonData("↓m", fmt.Sprintf("MINUS-MINUTE;%d;%d;%s", hour, minute, ampm)),
    tgbotapi.NewInlineKeyboardButtonData("±", fmt.Sprintf("MINUS-AMPM;%d;%d;%s", hour, minute, ampm)),
  )
  r4 := tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData("OK", fmt.Sprintf("OKAY;%d;%d;%s", hour, minute, ampm)),
  )
  return tgbotapi.InlineKeyboardMarkup{InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{r1, r2, r3, r4}}
}

func ProcessClock(q *tgbotapi.CallbackQuery) (bool, int, int, string) {
  parts := strings.Split(q.Data, ";")
  act := parts[0]
  if act == "ignore" {
    bot.Request(tgbotapi.NewCallback(q.ID, ""))
    return false, 0, 0, ""
  }
  h, _ := strconv.Atoi(parts[1])
  mi, _ := strconv.Atoi(parts[2])
  ap := parts[3]
  switch act {
  case "OKAY":
    bot.Request(tgbotapi.NewCallback(q.ID, ""))
    return true, h, mi, ap
  case "PLUS-HOUR":
    if h == 12 {
      h = 1
    } else {
      h++
    }
  case "MINUS-HOUR":
    if h <= 1 {
      h = 12
    } else {
      h--
    }
  case "PLUS-MINUTE":
    if mi >= 50 {
      mi = 0
    } else {
      mi += 10
    }
  case "MINUS-MINUTE":
    if mi < 10 {
      mi = 50
    } else {
      mi -= 10
    }
  case "PLUS-AMPM", "MINUS-AMPM":
    if ap == "am" {
      ap = "pm"
    } else {
      ap = "am"
    }
  }
  bot.Request(tgbotapi.NewEditMessageReplyMarkup(q.Message.Chat.ID, q.Message.MessageID, CreateClock(h, mi, ap)))
  return false, 0, 0, ""
}

// --------- 时区 ---------
func CreateTimezone(offset int) tgbotapi.InlineKeyboardMarkup {
  return tgbotapi.NewInlineKeyboardMarkup(
    tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("↑", fmt.Sprintf("PLUS;%d", offset))),
    tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("UTC %+d", offset), "ignore")),
    tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("↓", fmt.Sprintf("MINUS;%d", offset))),
    tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("OK", fmt.Sprintf("OKAY;%d", offset))),
  )
}

func ProcessUTC(q *tgbotapi.CallbackQuery) (bool, int) {
  parts := strings.Split(q.Data, ";")
  act := parts[0]
  off, _ := strconv.Atoi(parts[1])
  switch act {
  case "ignore":
    bot.Request(tgbotapi.NewCallback(q.ID, ""))
    return false, off
  case "PLUS":
    off++
  case "MINUS":
    off--
  case "OKAY":
    bot.Request(tgbotapi.NewCallback(q.ID, ""))
    return true, off
  }
  bot.Request(tgbotapi.NewEditMessageReplyMarkup(q.Message.Chat.ID, q.Message.MessageID, CreateTimezone(off)))
  return false, off
}

// --------- main ---------
func main() {
  cfg, err := loadConfig("config.json")
  if err != nil {
    log.Fatalf("load config.json failed: %v", err)
  }
  bot, err = tgbotapi.NewBotAPI(cfg.Token)
  if err != nil {
    log.Fatalf("new bot failed: %v", err)
  }
  bot.Debug = true
  log.Printf("Authorized on %s", bot.Self.UserName)

  if err := loadStorage(); err != nil {
    log.Fatalf("load reminder.json failed: %v", err)
  }

  // 恢复所有持久化的任务：一次性 + Cron
  for k, ud := range store.Reminder {
    chatID, _ := strconv.ParseInt(k, 10, 64)
    for _, r := range ud.Reminders {
      if r.CronExpr != "" {
        // 重新调度 cronexpr 任务
        loc, err := time.LoadLocation(r.TZ)
        if err != nil {
          continue
        }
        expr, err := cronexpr.Parse(r.CronOriginal)
        if err != nil {
          continue
        }
        quit := make(chan struct{})
        cronQuitMap[r.ID] = quit
        go runExprJob(chatID, r, expr, loc, quit)
      } else {
        scheduleOnce(chatID, r)
      }
    }
  }

  ucfg := tgbotapi.NewUpdate(0)
  ucfg.Timeout = 60
  updates := bot.GetUpdatesChan(ucfg)
  for upd := range updates {
    if upd.Message != nil {
      handleMessage(upd.Message)
    }
    if upd.CallbackQuery != nil {
      handleCallback(upd.CallbackQuery)
    }
  }
}