package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestResolveClientIP(t *testing.T) {
	r, err := NewIPResolver([]string{"10.0.0.0/8", "192.168.1.1"})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		remote string
		xff    string
		realIP string
		want   string
	}{
		{"direct client ignores header", "203.0.113.7:5000", "1.2.3.4", "", "203.0.113.7"},
		{"trusted proxy", "10.1.2.3:5000", "198.51.100.1", "", "198.51.100.1"},
		{"spoofed left part", "10.1.2.3:5000", "6.6.6.6, 198.51.100.1", "", "198.51.100.1"},
		{"chain of proxies", "192.168.1.1:80", "198.51.100.1, 10.0.0.5", "", "198.51.100.1"},
		{"garbage header", "10.1.2.3:5000", "not-an-ip", "", "10.1.2.3"},
		{"only proxies", "10.1.2.3:5000", "10.0.0.9", "", "10.1.2.3"},
		{"ipv6", "[2001:db8::1]:443", "", "", "2001:db8::1"},
		{"real ip from trusted proxy", "10.1.2.3:5000", "", "198.51.100.2", "198.51.100.2"},
		{"real ip ignored from direct client", "203.0.113.7:5000", "", "198.51.100.2", "203.0.113.7"},
		{"forwarded for wins over real ip", "10.1.2.3:5000", "198.51.100.1", "198.51.100.2", "198.51.100.1"},
		{"garbage real ip", "10.1.2.3:5000", "", "not-an-ip", "10.1.2.3"},
	}

	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.realIP != "" {
			req.Header.Set("X-Real-IP", c.realIP)
		}
		if got := r.Resolve(req).String(); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestNewIPResolverRejectsInvalid(t *testing.T) {
	if _, err := NewIPResolver([]string{"10.0.0.0/33"}); err == nil {
		t.Fatal("expected error")
	}
}
