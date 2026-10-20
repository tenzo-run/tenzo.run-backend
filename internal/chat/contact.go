package chat

import (
	"regexp"
	"strings"
)

var (
	emailPattern   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	telegramLink   = regexp.MustCompile(`(?i)(?:https?://)?(?:t\.me|telegram\.me)/([A-Za-z][A-Za-z0-9_]{4,31})`)
	handlePattern  = regexp.MustCompile(`(?:^|[\s,;:(])@([A-Za-z][A-Za-z0-9_]{4,31})\b`)
	phoneCandidate = regexp.MustCompile(`\+?\d[\d\s\-().]{8,18}\d`)
	whatsAppLink   = regexp.MustCompile(`(?i)(?:https?://)?wa\.me/(\d{10,15})`)
	bareHandle     = regexp.MustCompile(`^@?([A-Za-z][A-Za-z0-9_]{4,31})$`)
)

const (
	minPhoneDigits = 10
	maxPhoneDigits = 15
)

func FindContact(text string) string {
	if m := emailPattern.FindString(text); m != "" {
		return strings.ToLower(m)
	}
	if m := whatsAppLink.FindStringSubmatch(text); m != nil {
		if phone, ok := normalizePhone("+" + m[1]); ok {
			return phone
		}
	}
	for _, m := range phoneCandidate.FindAllString(text, -1) {
		if phone, ok := normalizePhone(m); ok {
			return phone
		}
	}
	if m := telegramLink.FindStringSubmatch(text); m != nil {
		return "@" + m[1]
	}
	if m := handlePattern.FindStringSubmatch(text); m != nil {
		return "@" + m[1]
	}
	return ""
}

func parseContact(raw string) string {
	raw = strings.TrimSpace(raw)
	if c := FindContact(raw); c != "" {
		return c
	}
	if m := bareHandle.FindStringSubmatch(raw); m != nil {
		return "@" + m[1]
	}
	return ""
}

func normalizePhone(raw string) (string, bool) {
	plus := strings.HasPrefix(strings.TrimSpace(raw), "+")
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()

	switch {
	case plus && len(d) >= minPhoneDigits+1 && len(d) <= maxPhoneDigits:
		return "+" + d, true
	case !plus && len(d) == 11 && (d[0] == '8' || d[0] == '7') && d[1] >= '6' && d[1] <= '9':
		return "+7" + d[1:], true
	case !plus && len(d) == 10 && d[0] == '7':
		return "+7" + d, true
	}
	return "", false
}

func FormatContact(c string) string {
	if len(c) == 12 && strings.HasPrefix(c, "+7") {
		return c[:2] + " " + c[2:5] + " " + c[5:8] + " " + c[8:10] + " " + c[10:]
	}
	return c
}
