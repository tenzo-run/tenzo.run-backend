package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const ipv6RatePrefix = 64

type clientIPKey struct{}

type IPResolver struct {
	trusted []netip.Prefix
}

func NewIPResolver(proxies []string) (*IPResolver, error) {
	trusted := make([]netip.Prefix, 0, len(proxies))

	for _, p := range proxies {
		prefix, err := parsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", p, err)
		}
		trusted = append(trusted, prefix)
	}

	return &IPResolver{trusted: trusted}, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func (r *IPResolver) Resolve(req *http.Request) netip.Addr {
	remote := remoteAddr(req.RemoteAddr)
	if !r.isTrusted(remote) {
		return remote
	}

	hops := forwardedHops(req.Header.Values("X-Forwarded-For"))
	if len(hops) == 0 {
		return realIP(req.Header.Get("X-Real-IP"), remote)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return remote
		}
		addr = addr.Unmap()
		if !r.isTrusted(addr) {
			return addr
		}
	}

	return remote
}

func (r *IPResolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), clientIPKey{}, r.Resolve(req))
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func (r *IPResolver) isTrusted(addr netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func ClientIP(ctx context.Context) netip.Addr {
	addr, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return addr
}

func RateKey(addr netip.Addr) string {
	if addr.Is6() {
		prefix, _ := addr.Prefix(ipv6RatePrefix)
		return prefix.String()
	}
	return addr.String()
}

func remoteAddr(s string) netip.Addr {
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		host = s
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func realIP(header string, fallback netip.Addr) netip.Addr {
	addr, err := netip.ParseAddr(strings.TrimSpace(header))
	if err != nil {
		return fallback
	}
	return addr.Unmap()
}

func forwardedHops(values []string) []string {
	var hops []string
	for _, v := range values {
		hops = append(hops, strings.Split(v, ",")...)
	}
	return hops
}
