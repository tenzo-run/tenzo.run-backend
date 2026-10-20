package chat

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/shared/apperr"
)

const (
	LocaleRU = "ru"
	LocaleEN = "en"
	LocaleZH = "zh"

	ChannelSite     = "site"
	ChannelTelegram = "telegram"

	RoleUser      = "user"
	RoleAssistant = "assistant"

	FromAdmin = "admin"

	maxNameLength     = 60
	maxContactLength  = 120
	maxProjectLength  = 600
	maxBudgetLength   = 120
	maxDeadlineLength = 120
	maxQuestionLength = 500
	maxPageLength     = 200
	maxProfileLength  = 120

	DefaultPageSize = 10
	MaxPageSize     = 50
)

var (
	ErrEmptyText = apperr.Invalid("message is empty")
	ErrLongText  = apperr.Invalid("message is too long")
	ErrBusy      = apperr.Conflict("previous message is still being answered")
	ErrTooLong   = apperr.TooManyRequests("chat is too long, please write directly")
	ErrNotFound  = apperr.NotFound("chat not found")
	ErrLimit     = apperr.Invalid("limit is out of range")
)

type Turn struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	From string    `json:"from,omitempty"`
}

type Lead struct {
	Name     string
	Contact  string
	Project  string
	Budget   string
	Deadline string
}

type Session struct {
	ID         int64
	PublicID   uuid.UUID
	Channel    string
	TelegramID int64
	Profile    string
	Locale     string
	Page       string
	Lead       Lead
	Turns      []Turn
	Waiting    bool
	Question   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	newContact bool
}

func (s *Session) adminTurns() []Turn {
	var out []Turn
	for _, t := range s.Turns {
		if t.From == FromAdmin {
			out = append(out, t)
		}
	}
	return out
}

func (s *Session) UserTurns() int {
	n := 0
	for _, t := range s.Turns {
		if t.Role == RoleUser {
			n++
		}
	}
	return n
}

func (s *Session) LastText() string {
	if len(s.Turns) == 0 {
		return ""
	}
	return s.Turns[len(s.Turns)-1].Text
}

type Updates struct {
	Messages []Turn
	Total    int
	Waiting  bool
}

type Filter struct {
	Query string
	Limit int
}

func (f *Filter) normalize() error {
	f.Query = strings.TrimSpace(f.Query)
	if f.Limit == 0 {
		f.Limit = DefaultPageSize
	}
	if f.Limit < 1 || f.Limit > MaxPageSize {
		return ErrLimit
	}
	return nil
}

type Input struct {
	Session  uuid.UUID
	Telegram int64
	Profile  string
	Contact  string
	Text     string
	Locale   string
	Page     string
}

func (in *Input) normalize(maxText int) error {
	in.Text = cleanText(in.Text)
	switch n := utf8.RuneCountInString(in.Text); {
	case n == 0:
		return ErrEmptyText
	case n > maxText:
		return ErrLongText
	}

	in.Locale = ParseLocale(in.Locale)
	in.Profile = clip(in.Profile, maxProfileLength)
	in.Contact = clip(in.Contact, maxContactLength)

	in.Page = strings.TrimSpace(in.Page)
	if !validPage(in.Page) {
		in.Page = ""
	}
	return nil
}

func ParseLocale(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(raw, LocaleZH):
		return LocaleZH
	case strings.HasPrefix(raw, LocaleEN):
		return LocaleEN
	default:
		return LocaleRU
	}
}

func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(cleanText(s)), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

func validPage(page string) bool {
	if page == "" {
		return true
	}
	return strings.HasPrefix(page, "/") &&
		utf8.RuneCountInString(page) <= maxPageLength &&
		!strings.ContainsFunc(page, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
