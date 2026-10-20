package telegram

const (
	maxText     = 4000
	notModified = "message is not modified"
)

type preview struct {
	Disabled bool `json:"is_disabled"`
}

func truncate(text string) string {
	runes := []rune(text)
	if len(runes) <= maxText {
		return text
	}
	return string(runes[:maxText]) + "…"
}
