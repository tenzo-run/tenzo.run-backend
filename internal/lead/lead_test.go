package lead

import (
	"errors"
	"strings"
	"testing"
)

func validDraft() Draft {
	return Draft{Name: "Анна", Contact: "@anna", Locale: LocaleRU, Page: "/ru"}
}

func TestDraftValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(d *Draft)
		want   error
	}{
		{"complete draft", func(*Draft) {}, nil},
		{"no page", func(d *Draft) { d.Page = "" }, nil},
		{"one letter name", func(d *Draft) { d.Name = "A" }, nil},
		{"longest name in runes", func(d *Draft) { d.Name = strings.Repeat("я", maxNameLength) }, nil},
		{"shortest contact", func(d *Draft) { d.Contact = "a@b" }, nil},
		{"longest contact in runes", func(d *Draft) { d.Contact = strings.Repeat("я", maxContactLength) }, nil},
		{"longest page", func(d *Draft) { d.Page = "/" + strings.Repeat("p", maxPageLength-1) }, nil},
		{"english", func(d *Draft) { d.Locale = LocaleEN }, nil},
		{"chinese", func(d *Draft) { d.Locale = LocaleZH }, nil},
		{"empty name", func(d *Draft) { d.Name = "" }, ErrInvalidName},
		{"long name", func(d *Draft) { d.Name = strings.Repeat("я", maxNameLength+1) }, ErrInvalidName},
		{"short contact", func(d *Draft) { d.Contact = "@a" }, ErrInvalidContact},
		{"long contact", func(d *Draft) { d.Contact = strings.Repeat("я", maxContactLength+1) }, ErrInvalidContact},
		{"unknown locale", func(d *Draft) { d.Locale = "de" }, ErrInvalidLocale},
		{"no locale", func(d *Draft) { d.Locale = "" }, ErrInvalidLocale},
		{"long page", func(d *Draft) { d.Page = strings.Repeat("p", maxPageLength+1) }, ErrInvalidPage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDraft()
			tc.mutate(&d)
			if err := d.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("validate = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDraftNormalizeTrims(t *testing.T) {
	d := Draft{Name: "  Анна \n", Contact: "\t+7 900 000-00-00 ", Locale: LocaleRU, Page: " /ru ", Website: "  "}
	d.normalize()

	if d.Name != "Анна" || d.Contact != "+7 900 000-00-00" || d.Page != "/ru" || d.Website != "" {
		t.Fatalf("draft = %+v", d)
	}
	if d.Spam() {
		t.Fatal("blank honeypot is not spam")
	}
}

func TestBlankNameIsInvalidAfterTrim(t *testing.T) {
	d := validDraft()
	d.Name, d.Contact = "   ", "  ab  "
	d.normalize()

	if err := d.Validate(); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("validate = %v", err)
	}
}

func TestSpam(t *testing.T) {
	d := validDraft()
	d.Website = "https://example.com"
	if !d.Spam() {
		t.Fatal("filled honeypot is spam")
	}
}

func TestLocaleValid(t *testing.T) {
	for _, l := range []Locale{LocaleRU, LocaleEN, LocaleZH} {
		if !l.Valid() {
			t.Errorf("%q must be valid", l)
		}
	}
	for _, l := range []Locale{"", "RU", "de", "zh-CN"} {
		if l.Valid() {
			t.Errorf("%q must be invalid", l)
		}
	}
}
