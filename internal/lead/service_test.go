package lead

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/shared/apperr"
)

type journal struct {
	events []string
}

type storeStub struct {
	journal *journal
	saved   []Lead
	latests []int
	err     error
}

func (s *storeStub) insert(_ context.Context, l Lead) error {
	s.journal.events = append(s.journal.events, "insert")
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, l)
	return nil
}

func (s *storeStub) latest(_ context.Context, limit int) ([]Lead, error) {
	s.latests = append(s.latests, limit)
	return s.saved, s.err
}

type notifierStub struct {
	journal *journal
	texts   []string
}

func (n *notifierStub) Send(text string) {
	n.journal.events = append(n.journal.events, "notify")
	n.texts = append(n.texts, text)
}

var submittedAt = time.Date(2026, 10, 12, 9, 5, 0, 0, time.UTC)

func newTestService(storeErr error) (*Service, *storeStub, *notifierStub, *journal) {
	j := &journal{}
	store := &storeStub{journal: j, err: storeErr}
	notifier := &notifierStub{journal: j}
	s := NewService(store, notifier)
	s.now = func() time.Time { return submittedAt }
	return s, store, notifier, j
}

func TestSubmitSavesThenNotifies(t *testing.T) {
	s, store, notifier, j := newTestService(nil)
	ip := netip.MustParseAddr("203.0.113.10")

	id, err := s.Submit(t.Context(), Draft{Name: " Анна ", Contact: " @anna ", Locale: LocaleRU, Page: "/ru/contact", IP: ip})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(j.events, []string{"insert", "notify"}) {
		t.Fatalf("events = %v", j.events)
	}
	if id.Version() != 7 {
		t.Fatalf("id %s is not a uuid v7", id)
	}

	want := Lead{ID: id, Name: "Анна", Contact: "@anna", Locale: LocaleRU, Page: "/ru/contact", IP: ip, CreatedAt: submittedAt}
	if len(store.saved) != 1 || store.saved[0] != want {
		t.Fatalf("saved = %+v, want %+v", store.saved, want)
	}

	text := notifier.texts[0]
	for _, part := range []string{"Имя: Анна", "Контакт: @anna", "Язык: ru", "Страница: /ru/contact", "Время: 12.10.2026 09:05 UTC"} {
		if !strings.Contains(text, part) {
			t.Errorf("notice %q has no %q", text, part)
		}
	}
}

func TestSubmitWithoutPageShowsDash(t *testing.T) {
	s, _, notifier, _ := newTestService(nil)

	if _, err := s.Submit(t.Context(), Draft{Name: "Ann", Contact: "ann@example.com", Locale: LocaleEN}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notifier.texts[0], "Страница: "+noPage) {
		t.Fatalf("notice = %q", notifier.texts[0])
	}
}

func TestSubmitSpamStoresAndSendsNothing(t *testing.T) {
	s, store, notifier, j := newTestService(nil)

	id, err := s.Submit(t.Context(), Draft{Name: "", Contact: "x", Locale: "de", Website: "https://spam.example"})
	if err != nil {
		t.Fatalf("spam must look accepted: %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("spam must get a fresh id")
	}
	if len(j.events) != 0 || len(store.saved) != 0 || len(notifier.texts) != 0 {
		t.Fatalf("spam reached store or notifier: %v", j.events)
	}
}

func TestSubmitRejectsInvalidDraft(t *testing.T) {
	cases := []struct {
		name  string
		draft Draft
		want  error
	}{
		{"blank name", Draft{Name: "   ", Contact: "@anna", Locale: LocaleRU}, ErrInvalidName},
		{"short contact after trim", Draft{Name: "Анна", Contact: "  @a  ", Locale: LocaleRU}, ErrInvalidContact},
		{"unknown locale", Draft{Name: "Анна", Contact: "@anna", Locale: "de"}, ErrInvalidLocale},
		{"long page", Draft{Name: "Анна", Contact: "@anna", Locale: LocaleRU, Page: strings.Repeat("p", maxPageLength+1)}, ErrInvalidPage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, j := newTestService(nil)

			id, err := s.Submit(t.Context(), tc.draft)
			if !errors.Is(err, tc.want) || id != uuid.Nil {
				t.Fatalf("submit = %s, %v; want %v", id, err, tc.want)
			}
			if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindInvalid {
				t.Fatalf("error %v is not an invalid input error", err)
			}
			if len(j.events) != 0 {
				t.Fatalf("invalid draft reached store or notifier: %v", j.events)
			}
		})
	}
}

func TestSubmitDoesNotNotifyWhenSaveFails(t *testing.T) {
	broken := errors.New("connection reset")
	s, _, notifier, _ := newTestService(broken)

	_, err := s.Submit(t.Context(), validDraft())
	if !errors.Is(err, broken) {
		t.Fatalf("submit = %v", err)
	}
	if _, ok := apperr.As(err); ok {
		t.Fatal("storage failure must stay internal")
	}
	if len(notifier.texts) != 0 {
		t.Fatal("notified about a lead that was not saved")
	}
}

func TestSubmitWithoutNotifier(t *testing.T) {
	store := &storeStub{journal: &journal{}}
	s := NewService(store, nil)

	if _, err := s.Submit(t.Context(), validDraft()); err != nil || len(store.saved) != 1 {
		t.Fatalf("submit = %v, saved %d", err, len(store.saved))
	}
}

func TestLatestChecksLimit(t *testing.T) {
	for _, limit := range []int{0, -1, MaxListLimit + 1} {
		s, store, _, _ := newTestService(nil)
		if _, err := s.Latest(t.Context(), limit); !errors.Is(err, ErrInvalidLimit) || len(store.latests) != 0 {
			t.Errorf("limit %d: %v", limit, err)
		}
	}

	s, store, _, _ := newTestService(nil)
	if _, err := s.Latest(t.Context(), DefaultListLimit); err != nil || !slices.Equal(store.latests, []int{DefaultListLimit}) {
		t.Fatalf("latest = %v, limits %v", err, store.latests)
	}
}
