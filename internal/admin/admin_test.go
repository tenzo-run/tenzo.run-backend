package admin

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/chat"
	"tenzo-run-backend/internal/shared/telegram"
)

func session(lead chat.Lead) chat.Session {
	return chat.Session{
		ID: 7, PublicID: uuid.MustParse("0190a3c0-0000-7000-8000-00000000beef"), Channel: chat.ChannelSite,
		Locale: chat.LocaleRU, Page: "/ru/ceny", Lead: lead,
		Turns: []chat.Turn{{Role: chat.RoleUser, Text: "Сколько стоит лендинг?"}},
	}
}

func TestHeadlineShowsWhatHappened(t *testing.T) {
	s := session(chat.Lead{})
	if got := headline(chat.Notice{Session: s}); got != "🆕 Новый посетитель · сайт RU · /ru/ceny" {
		t.Fatalf("new = %q", got)
	}
	if got := headline(chat.Notice{Session: s, NewContact: true}); !strings.HasPrefix(got, "✅ Оставил контакт") {
		t.Fatalf("contact = %q", got)
	}
	s.Channel = chat.ChannelTelegram
	if got := headline(chat.Notice{Session: s, Returning: true}); got != "🔁 Снова пишет · Telegram-бот" {
		t.Fatalf("telegram = %q", got)
	}
}

func TestLabelPrefersNameThenProfileThenContact(t *testing.T) {
	if got := label(session(chat.Lead{})); got != "Посетитель BEEF" {
		t.Fatalf("anonymous = %q", got)
	}
	if got := label(session(chat.Lead{Contact: "+77000600054"})); got != "+7 700 060 00 54" {
		t.Fatalf("contact = %q", got)
	}
	s := session(chat.Lead{Name: "Айдар", Contact: "@aidar_kz"})
	s.Profile = "Aidar (@aidar_kz)"
	if got := label(s); got != "Айдар" {
		t.Fatalf("name = %q", got)
	}
}

func TestKeyboardAsksOnlyForWhatIsMissing(t *testing.T) {
	buttons := func(m *telegram.Markup) string {
		var out []string
		for _, row := range m.InlineKeyboard {
			for _, b := range row {
				out = append(out, b.Text+"="+b.CallbackData+b.URL)
			}
		}
		return strings.Join(out, " ")
	}

	empty := buttons(keyboard(chat.Notice{Session: session(chat.Lead{})}))
	for _, want := range []string{"📇 Контакт=ct:7", "🙂 Имя=nm:7", "📞 Созвон=cl:7", "✏️ Ответить=rp:7"} {
		if !strings.Contains(empty, want) {
			t.Errorf("keyboard %q has no %q", empty, want)
		}
	}

	known := buttons(keyboard(chat.Notice{Session: session(chat.Lead{Name: "Айдар", Contact: "+77000600054"})}))
	if strings.Contains(known, "Контакт") || strings.Contains(known, "Имя") || !strings.Contains(known, "https://wa.me/77000600054") {
		t.Fatalf("keyboard = %q", known)
	}
}

func TestSummaryEscalationAndEscaping(t *testing.T) {
	s := session(chat.Lead{Project: "Сайт <script>", Budget: "300 000", Deadline: "к декабрю"})
	s.Question = "Есть ли рассрочка"
	text := summary(chat.Notice{Session: s, Text: "a & b", Reply: "Уточню у Санжара", Escalated: true})
	for _, want := range []string{"Сайт &lt;script&gt;", "<b>Бюджет:</b> 300 000 · <b>Срок:</b> к декабрю", "👤 a &amp; b", "❓ <b>Ждёт вашего ответа:</b> Есть ли рассрочка"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary has no %q:\n%s", want, text)
		}
	}
}

func TestQuickReplyFallsBackToRussian(t *testing.T) {
	if text, ok := quickReply(ActionOfferCall, chat.LocaleEN); !ok || !strings.HasPrefix(text, "Let's have") {
		t.Fatalf("en = %q", text)
	}
	if text, ok := quickReply(ActionAskName, "kk"); !ok || text != "Как к вам можно обращаться?" {
		t.Fatalf("fallback = %q", text)
	}
	if _, ok := quickReply(ActionReply, chat.LocaleRU); ok {
		t.Fatal("reply is not a quick reply")
	}
}

func TestAdminsAllowByIDOrUsername(t *testing.T) {
	a := NewAdmins(nil, []string{"42", "x", "42"}, []string{"@Tenzo_Run", " "})
	cases := []struct {
		user telegram.User
		want bool
	}{
		{telegram.User{ID: 42}, true},
		{telegram.User{ID: 1, Username: "tenzo_run"}, true},
		{telegram.User{ID: 2, Username: "TENZO_RUN"}, true},
		{telegram.User{ID: 3, Username: "someone"}, false},
		{telegram.User{ID: 4}, false},
	}
	for _, tc := range cases {
		if got := a.Allowed(tc.user); got != tc.want {
			t.Errorf("Allowed(%+v) = %v", tc.user, got)
		}
	}
	if len(a.ids) != 1 {
		t.Fatalf("ids = %v", a.ids)
	}
}

func TestCallbackDataRoundTrip(t *testing.T) {
	a, id, ok := parseCallback(callbackData(ActionReply, "15"))
	if !ok || a != ActionReply || id != "15" {
		t.Fatalf("parsed = %q %q %v", a, id, ok)
	}
	if _, _, ok := parseCallback("broken"); ok {
		t.Fatal("broken data must not parse")
	}
}
