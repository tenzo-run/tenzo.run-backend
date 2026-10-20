package chat

import "testing"

func TestFindContact(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"мой номер +7 700 060 00 54", "+77000600054"},
		{"звоните 8 (701) 123-45-67", "+77011234567"},
		{"7011234567 whatsapp", "+77011234567"},
		{"wa.me/77000600054", "+77000600054"},
		{"Dubai +971 50 123 4567", "+971501234567"},
		{"пишите на Hello@Tenzo.Run", "hello@tenzo.run"},
		{"мой телеграм @aidar_kz", "@aidar_kz"},
		{"https://t.me/aidar_kz", "@aidar_kz"},
		{"бюджет 2 500 000 - 3 000 000 тенге", ""},
		{"срок 700 000 000 000", ""},
		{"почта не нужна, просто @ab", ""},
		{"Сколько стоит лендинг?", ""},
	}
	for _, tc := range cases {
		if got := FindContact(tc.text); got != tc.want {
			t.Errorf("FindContact(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestParseContactAcceptsBareHandle(t *testing.T) {
	if got := parseContact("aidar_kz"); got != "@aidar_kz" {
		t.Fatalf("handle = %q", got)
	}
	if got := parseContact("не знаю"); got != "" {
		t.Fatalf("garbage = %q", got)
	}
}

func TestFormatContact(t *testing.T) {
	if got := FormatContact("+77000600054"); got != "+7 700 060 00 54" {
		t.Fatalf("phone = %q", got)
	}
	if got := FormatContact("@aidar_kz"); got != "@aidar_kz" {
		t.Fatalf("handle = %q", got)
	}
}

func TestParseLocale(t *testing.T) {
	for raw, want := range map[string]string{"ru": LocaleRU, "en-US": LocaleEN, "zh-hans": LocaleZH, "kk": LocaleRU, "": LocaleRU} {
		if got := ParseLocale(raw); got != want {
			t.Errorf("ParseLocale(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTopicOf(t *testing.T) {
	if got := topicOf("/ru/uslugi/ii-assistenty"); got != "ИИ-ассистенты" {
		t.Fatalf("topic = %q", got)
	}
	if got := topicOf("/en"); got != "" {
		t.Fatalf("home topic = %q", got)
	}
}
