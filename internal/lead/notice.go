package lead

import (
	"fmt"
	"strings"
)

const (
	noticeTitle      = "Новая заявка с tenzo.run"
	noticeTimeLayout = "02.01.2006 15:04 UTC"
	noPage           = "—"
)

func notice(l Lead) string {
	page := l.Page
	if page == "" {
		page = noPage
	}

	message := l.Message
	if message == "" {
		message = noPage
	}

	var b strings.Builder
	b.WriteString(noticeTitle)
	for _, field := range []struct{ label, value string }{
		{"Имя", l.Name},
		{"Контакт", l.Contact},
		{"Язык", string(l.Locale)},
		{"Страница", page},
		{"Задача", message},
		{"Время", l.CreatedAt.UTC().Format(noticeTimeLayout)},
	} {
		fmt.Fprintf(&b, "\n%s: %s", field.label, field.value)
	}
	return b.String()
}
