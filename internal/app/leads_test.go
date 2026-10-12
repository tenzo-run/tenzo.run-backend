package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/config"
)

const leadsPath = "/api/v1/leads"

type createdBody struct {
	ID string `json:"id"`
}

type problemBody struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func from(ip string) http.Header {
	return http.Header{"X-Forwarded-For": {ip}}
}

func leadBody(name string) map[string]string {
	return map[string]string{"name": name, "contact": "@" + strings.ToLower(name), "locale": "ru", "page": "/ru"}
}

func (s *testServer) submitLead(body map[string]string, ip string) uuid.UUID {
	s.t.Helper()
	var out createdBody
	s.expect(request{method: "POST", path: leadsPath, body: body, header: from(ip)}, 201, &out)
	id, err := uuid.Parse(out.ID)
	if err != nil {
		s.t.Fatalf("id %q: %v", out.ID, err)
	}
	return id
}

func TestCreateLeadStoresAndNotifies(t *testing.T) {
	s := startServer(t)

	res := s.expect(request{method: "POST", path: leadsPath, header: from("203.0.113.10"), body: map[string]string{
		"name":    "  Анна  ",
		"contact": " t.me/anna ",
		"locale":  "en",
		"page":    "/en/contact",
	}}, 201, nil)

	var out createdBody
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatal(err)
	}
	id, err := uuid.Parse(out.ID)
	if err != nil || id.Version() != 7 {
		t.Fatalf("id = %q, %v", out.ID, err)
	}

	var (
		name, contact, locale, page string
		ip                          netip.Addr
		createdAt                   time.Time
	)
	err = s.pool.QueryRow(t.Context(),
		`select name, contact, locale::text, page, ip, created_at from leads where public_id = $1`, id,
	).Scan(&name, &contact, &locale, &page, &ip, &createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Анна" || contact != "t.me/anna" || locale != "en" || page != "/en/contact" {
		t.Fatalf("row = %q %q %q %q", name, contact, locale, page)
	}
	if ip != netip.MustParseAddr("203.0.113.10") {
		t.Fatalf("ip = %s", ip)
	}
	if time.Since(createdAt) > time.Minute {
		t.Fatalf("created at = %s", createdAt)
	}

	text := s.message()
	for _, part := range []string{"Имя: Анна", "Контакт: t.me/anna", "Язык: en", "Страница: /en/contact", "Время: "} {
		if !strings.Contains(text, part) {
			t.Errorf("message %q has no %q", text, part)
		}
	}
}

func TestCreateLeadWithoutPage(t *testing.T) {
	s := startServer(t)

	id := s.submitLead(map[string]string{"name": "Li", "contact": "li@example.com", "locale": "zh"}, "203.0.113.11")

	var page string
	if err := s.pool.QueryRow(t.Context(), `select page from leads where public_id = $1`, id).Scan(&page); err != nil {
		t.Fatal(err)
	}
	if page != "" {
		t.Fatalf("page = %q", page)
	}
	if text := s.message(); !strings.Contains(text, "Страница: —") {
		t.Fatalf("message = %q", text)
	}
}

func TestCreateLeadValidation(t *testing.T) {
	s := startServer(t)

	cases := []struct {
		name string
		body map[string]string
		raw  string
	}{
		{"missing name", map[string]string{"contact": "@anna", "locale": "ru"}, ""},
		{"blank name", map[string]string{"name": "   ", "contact": "@anna", "locale": "ru"}, ""},
		{"long name", map[string]string{"name": strings.Repeat("я", 101), "contact": "@anna", "locale": "ru"}, ""},
		{"missing contact", map[string]string{"name": "Анна", "locale": "ru"}, ""},
		{"short contact", map[string]string{"name": "Анна", "contact": "@a", "locale": "ru"}, ""},
		{"short contact after trim", map[string]string{"name": "Анна", "contact": "  ab  ", "locale": "ru"}, ""},
		{"long contact", map[string]string{"name": "Анна", "contact": strings.Repeat("я", 201), "locale": "ru"}, ""},
		{"unknown locale", map[string]string{"name": "Анна", "contact": "@anna", "locale": "de"}, ""},
		{"missing locale", map[string]string{"name": "Анна", "contact": "@anna"}, ""},
		{"long page", map[string]string{"name": "Анна", "contact": "@anna", "locale": "ru", "page": strings.Repeat("p", 301)}, ""},
		{"long honeypot", map[string]string{"name": "Анна", "contact": "@anna", "locale": "ru", "website": strings.Repeat("w", 201)}, ""},
		{"unknown field", map[string]string{"name": "Анна", "contact": "@anna", "locale": "ru", "email": "a@b.c"}, ""},
		{"locale is not a string", nil, `{"name":"Анна","contact":"@anna","locale":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request{method: "POST", path: leadsPath, raw: tc.raw, header: from("203.0.113.12")}
			if tc.body != nil {
				req.body = tc.body
			}
			res := s.expect(req, 422, nil)

			if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("content type = %q", ct)
			}
			var p problemBody
			if err := json.Unmarshal(res.body, &p); err != nil || p.Status != 422 || p.Title == "" {
				t.Fatalf("problem = %+v, %v: %s", p, err, res.body)
			}
		})
	}

	if n := s.countLeads(); n != 0 {
		t.Fatalf("invalid requests stored %d leads", n)
	}
}

func TestCreateLeadRejectsMalformedJSON(t *testing.T) {
	s := startServer(t)

	res := s.expect(request{method: "POST", path: leadsPath, raw: `{"name":`, header: from("203.0.113.13")}, 400, nil)
	if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}
	if n := s.countLeads(); n != 0 {
		t.Fatalf("stored %d leads", n)
	}
}

func TestHoneypotStoresNothing(t *testing.T) {
	s := startServer(t)

	body := leadBody("Bot")
	body["website"] = "https://spam.example"
	spamID := s.submitLead(body, "203.0.113.14")

	if n := s.countLeads(); n != 0 {
		t.Fatalf("spam stored %d leads", n)
	}

	realID := s.submitLead(leadBody("Human"), "203.0.113.14")
	if spamID == realID {
		t.Fatal("spam and real lead share an id")
	}
	if text := s.message(); !strings.Contains(text, "Имя: Human") {
		t.Fatalf("first message = %q, spam must not be sent", text)
	}
	if n := s.countLeads(); n != 1 {
		t.Fatalf("stored %d leads", n)
	}
}

func TestCreateLeadRateLimit(t *testing.T) {
	s := startServer(t, func(c *config.Config) {
		c.Limits.LeadsPerIP = config.Rate{Events: 2, Per: time.Hour}
	})

	s.submitLead(leadBody("One"), "198.51.100.20")
	s.submitLead(leadBody("Two"), "198.51.100.20")

	res := s.expect(request{method: "POST", path: leadsPath, body: leadBody("Three"), header: from("198.51.100.20")}, 429, nil)
	retry, err := strconv.Atoi(res.header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > int(time.Hour/time.Second) {
		t.Fatalf("Retry-After = %q", res.header.Get("Retry-After"))
	}
	if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}

	s.submitLead(leadBody("Other"), "198.51.100.21")

	if n := s.countLeads(); n != 3 {
		t.Fatalf("stored %d leads, want 3", n)
	}
}

func TestCreateLeadFromAllowedOrigin(t *testing.T) {
	s := startServer(t)

	preflight := s.expect(request{method: "OPTIONS", path: leadsPath, header: http.Header{
		"Origin":                         {testOrigin},
		"Access-Control-Request-Method":  {"POST"},
		"Access-Control-Request-Headers": {"content-type"},
	}}, 204, nil)
	if preflight.header.Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatalf("preflight headers = %v", preflight.header)
	}

	s.expect(request{method: "POST", path: leadsPath, body: leadBody("Site"), header: http.Header{
		"Origin":         {testOrigin},
		"Sec-Fetch-Site": {"same-site"},
	}}, 201, nil)

	s.expect(request{method: "POST", path: leadsPath, body: leadBody("Evil"), header: http.Header{
		"Origin":         {"https://evil.example"},
		"Sec-Fetch-Site": {"cross-site"},
	}}, 403, nil)

	if n := s.countLeads(); n != 1 {
		t.Fatalf("stored %d leads, want 1", n)
	}
}

func TestLatestLeadsNewestFirst(t *testing.T) {
	s := startServer(t)

	var buf bytes.Buffer
	if err := printLatestLeads(t.Context(), s.svc.leads, 20, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != noLeads {
		t.Fatalf("empty listing = %q", buf.String())
	}

	for _, name := range []string{"First", "Second", "Third"} {
		s.submitLead(leadBody(name), "203.0.113.15")
	}

	buf.Reset()
	if err := printLatestLeads(t.Context(), s.svc.leads, 20, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "TIME") {
		t.Fatalf("listing = %q", buf.String())
	}
	for i, name := range []string{"Third", "Second", "First"} {
		row := lines[i+1]
		if !strings.Contains(row, name) || !strings.Contains(row, "@"+strings.ToLower(name)) || !strings.Contains(row, "ru") || !strings.Contains(row, "/ru") {
			t.Fatalf("row %d = %q, want %s", i+1, row, name)
		}
	}

	buf.Reset()
	if err := printLatestLeads(t.Context(), s.svc.leads, 2, &buf); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(buf.String()), "\n"); len(lines) != 3 || strings.Contains(buf.String(), "First") {
		t.Fatalf("limited listing = %q", buf.String())
	}

	if err := printLatestLeads(t.Context(), s.svc.leads, 0, &buf); err == nil {
		t.Fatal("zero limit must be rejected")
	}
}
