package admin

import (
	"html"
	"strconv"
	"strings"

	"tenzo-run-backend/internal/chat"
)

type Action string

const (
	ActionAskContact Action = "ct"
	ActionAskName    Action = "nm"
	ActionOfferCall  Action = "cl"
	ActionReply      Action = "rp"
	ActionApplyRules Action = "ra"
	ActionDropRules  Action = "rc"

	callbackSep    = ":"
	anonymousLabel = "Посетитель"
	maxPreview     = 600
	maxSummary     = 600
)

var quickReplies = map[Action]map[string]string{
	ActionAskContact: {
		chat.LocaleRU: "Оставьте, пожалуйста, Telegram, WhatsApp или телефон — напишу лично и пришлю план со сметой.",
		chat.LocaleEN: "Please leave your Telegram, WhatsApp or phone number, and I will write to you personally with a plan and an estimate.",
		chat.LocaleZH: "请留下您的 Telegram、WhatsApp 或电话号码，我会亲自联系您，并发送方案和报价。",
	},
	ActionAskName: {
		chat.LocaleRU: "Как к вам можно обращаться?",
		chat.LocaleEN: "How may I address you?",
		chat.LocaleZH: "请问怎么称呼您？",
	},
	ActionOfferCall: {
		chat.LocaleRU: "Давайте созвонимся на 15–30 минут и разберём задачу. Когда вам удобно?",
		chat.LocaleEN: "Let's have a 15–30 minute call to go through your project. When would suit you?",
		chat.LocaleZH: "我们约个 15–30 分钟的电话，详细聊聊您的项目吧。您什么时候方便？",
	},
}

var signatures = map[string]string{
	chat.LocaleRU: "Санжар",
	chat.LocaleEN: "Sanzhar",
	chat.LocaleZH: "Sanzhar",
}

var localeLabels = map[string]string{
	chat.LocaleRU: "RU",
	chat.LocaleEN: "EN",
	chat.LocaleZH: "ZH",
}

func quickReply(a Action, locale string) (string, bool) {
	texts, ok := quickReplies[a]
	if !ok {
		return "", false
	}
	if text, ok := texts[locale]; ok {
		return text, true
	}
	return texts[chat.LocaleRU], true
}

func callbackData(a Action, id string) string {
	return string(a) + callbackSep + id
}

func parseCallback(data string) (Action, string, bool) {
	a, id, ok := strings.Cut(data, callbackSep)
	if !ok || id == "" {
		return "", "", false
	}
	return Action(a), id, true
}

func parseID(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

func headline(n chat.Notice) string {
	var title string
	switch {
	case n.NewContact:
		title = "✅ Оставил контакт"
	case n.Returning:
		title = "🔁 Снова пишет"
	case n.Session.UserTurns() > 1:
		title = "💬 Пишет дальше"
	default:
		title = "🆕 Новый посетитель"
	}
	return title + " · " + origin(n.Session)
}

func origin(s chat.Session) string {
	if s.Channel == chat.ChannelTelegram {
		return "Telegram-бот"
	}
	where := "сайт " + localeLabels[s.Locale]
	if s.Page != "" {
		where += " · " + s.Page
	}
	return where
}

func label(s chat.Session) string {
	switch {
	case s.Lead.Name != "":
		return s.Lead.Name
	case s.Profile != "":
		return s.Profile
	case s.Lead.Contact != "":
		return chat.FormatContact(s.Lead.Contact)
	default:
		id := s.PublicID.String()
		return anonymousLabel + " " + strings.ToUpper(id[len(id)-4:])
	}
}

func esc(s string) string {
	return html.EscapeString(s)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}
