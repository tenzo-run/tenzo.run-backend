package admin

import "tenzo-run-backend/internal/chat"

type clientKind int

const (
	greeting clientKind = iota
	restarted
	textOnly
	busy
	tooLong
	longText
	failed
)

var clientTexts = map[clientKind]map[string]string{
	greeting: {
		chat.LocaleRU: "Здравствуйте! Я ИИ-ассистент Tenzo — Санжара Абдурахманова, разработчика сайтов, веб-сервисов, ИИ-ассистентов и мобильных приложений.\n\nРасскажите, что хотите сделать, — подскажу по срокам и цене, а Санжар лично пришлёт план и смету за один рабочий день. Он видит этот диалог и может ответить здесь же.",
		chat.LocaleEN: "Hello! I am the AI assistant of Tenzo — Sanzhar Abdurakhmanov, a developer of websites, web services, AI assistants and mobile apps.\n\nTell me what you would like to build, and I will give you a sense of timing and cost. Sanzhar will personally send you a plan and an estimate within one working day. He sees this chat and can reply right here.",
		chat.LocaleZH: "您好！我是 Tenzo 的 AI 助手。Tenzo 是 Sanzhar Abdurakhmanov，专注于网站、Web 服务、AI 助手和移动应用开发。\n\n请告诉我您想做什么，我会介绍大致的周期和价格，Sanzhar 会在一个工作日内亲自发送方案和报价。他能看到这段对话，也可以直接在这里回复您。",
	},
	restarted: {
		chat.LocaleRU: "Начали заново. Расскажите, какую задачу хотите решить.",
		chat.LocaleEN: "Starting over. Tell me what you would like to build.",
		chat.LocaleZH: "我们重新开始。请告诉我您想解决什么问题。",
	},
	textOnly: {
		chat.LocaleRU: "Пока я понимаю только текст. Напишите, пожалуйста, задачу словами — а файлы и голосовые Санжар посмотрит, когда напишет вам лично.",
		chat.LocaleEN: "For now I only understand text. Please describe your project in words; Sanzhar will look at files and voice notes when he writes to you personally.",
		chat.LocaleZH: "目前我只能理解文字。请用文字描述您的需求，文件和语音消息 Sanzhar 会在亲自联系您时查看。",
	},
	busy: {
		chat.LocaleRU: "Секунду, ещё отвечаю на предыдущее сообщение.",
		chat.LocaleEN: "One moment, I am still answering your previous message.",
		chat.LocaleZH: "请稍等，我还在回复您上一条消息。",
	},
	tooLong: {
		chat.LocaleRU: "Диалог получился длинным — дальше лучше напрямую с Санжаром: Telegram @tenzo_run, WhatsApp +7 700 060 00 54.",
		chat.LocaleEN: "This conversation has become long, so it is better to continue with Sanzhar directly: Telegram @tenzo_run, WhatsApp +7 700 060 00 54.",
		chat.LocaleZH: "对话已经很长了，建议直接联系 Sanzhar：Telegram @tenzo_run，WhatsApp +7 700 060 00 54。",
	},
	longText: {
		chat.LocaleRU: "Сообщение слишком длинное. Опишите задачу короче или пришлите подробности Санжару напрямую: @tenzo_run.",
		chat.LocaleEN: "The message is too long. Please describe the project more briefly or send the details to Sanzhar directly: @tenzo_run.",
		chat.LocaleZH: "消息太长了。请简要描述需求，或直接把详情发给 Sanzhar：@tenzo_run。",
	},
	failed: {
		chat.LocaleRU: "Не получилось ответить прямо сейчас. Санжар уже видит ваше сообщение, или напишите ему напрямую: @tenzo_run.",
		chat.LocaleEN: "I could not answer right now. Sanzhar already sees your message, or you can write to him directly: @tenzo_run.",
		chat.LocaleZH: "暂时无法回复。Sanzhar 已经能看到您的消息，您也可以直接联系他：@tenzo_run。",
	},
}

func clientText(k clientKind, locale string) string {
	texts := clientTexts[k]
	if text, ok := texts[locale]; ok {
		return text
	}
	return texts[chat.LocaleRU]
}
