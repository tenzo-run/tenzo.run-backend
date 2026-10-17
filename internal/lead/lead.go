package lead

import (
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/shared/apperr"
)

const (
	minNameLength    = 1
	maxNameLength    = 100
	minContactLength = 3
	maxContactLength = 200
	maxPageLength    = 300
	maxMessageLength = 2000

	DefaultListLimit = 20
	MaxListLimit     = 500
)

var (
	ErrInvalidName    = apperr.Invalid("name must be 1 to 100 characters")
	ErrInvalidContact = apperr.Invalid("contact must be 3 to 200 characters")
	ErrInvalidLocale  = apperr.Invalid("locale must be ru, en or zh")
	ErrInvalidPage    = apperr.Invalid("page must not exceed 300 characters")
	ErrInvalidMessage = apperr.Invalid("message must not exceed 2000 characters")
	ErrInvalidLimit   = apperr.Invalid("limit must be between 1 and 500")
)

type Locale string

const (
	LocaleRU Locale = "ru"
	LocaleEN Locale = "en"
	LocaleZH Locale = "zh"
)

func (l Locale) Valid() bool {
	return l == LocaleRU || l == LocaleEN || l == LocaleZH
}

type Lead struct {
	ID        uuid.UUID
	Name      string
	Contact   string
	Locale    Locale
	Page      string
	Message   string
	IP        netip.Addr
	CreatedAt time.Time
}

type Draft struct {
	Name    string
	Contact string
	Locale  Locale
	Page    string
	Message string
	Website string
	IP      netip.Addr
}

func (d *Draft) normalize() {
	d.Name = strings.TrimSpace(d.Name)
	d.Contact = strings.TrimSpace(d.Contact)
	d.Page = strings.TrimSpace(d.Page)
	d.Message = strings.TrimSpace(d.Message)
	d.Website = strings.TrimSpace(d.Website)
}

func (d Draft) Spam() bool {
	return d.Website != ""
}

func (d Draft) Validate() error {
	switch {
	case !between(d.Name, minNameLength, maxNameLength):
		return ErrInvalidName
	case !between(d.Contact, minContactLength, maxContactLength):
		return ErrInvalidContact
	case !d.Locale.Valid():
		return ErrInvalidLocale
	case !between(d.Page, 0, maxPageLength):
		return ErrInvalidPage
	case !between(d.Message, 0, maxMessageLength):
		return ErrInvalidMessage
	}
	return nil
}

func (d Draft) lead(id uuid.UUID, createdAt time.Time) Lead {
	return Lead{
		ID:        id,
		Name:      d.Name,
		Contact:   d.Contact,
		Locale:    d.Locale,
		Page:      d.Page,
		Message:   d.Message,
		IP:        d.IP,
		CreatedAt: createdAt,
	}
}

func validLimit(limit int) bool {
	return limit >= 1 && limit <= MaxListLimit
}

func between(s string, minimum, maximum int) bool {
	n := utf8.RuneCountInString(s)
	return n >= minimum && n <= maximum
}
