package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestConstructors(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		kind Kind
	}{
		{"invalid", Invalid("payload is wrong"), KindInvalid},
		{"unauthorized", Unauthorized("payload is wrong"), KindUnauthorized},
		{"forbidden", Forbidden("payload is wrong"), KindForbidden},
		{"not found", NotFound("payload is wrong"), KindNotFound},
		{"conflict", Conflict("payload is wrong"), KindConflict},
		{"too many requests", TooManyRequests("payload is wrong"), KindTooManyRequests},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Kind != tc.kind || tc.err.Error() != "payload is wrong" {
				t.Fatalf("error = %+v, want kind %d", tc.err, tc.kind)
			}
		})
	}
}

func TestAs(t *testing.T) {
	taken := Conflict("email is taken")

	cases := []struct {
		name string
		err  error
		want *Error
	}{
		{"application error", taken, taken},
		{"wrapped application error", fmt.Errorf("create user: %w", taken), taken},
		{"joined application error", errors.Join(errors.New("audit failed"), taken), taken},
		{"plain error", errors.New("connection reset"), nil},
		{"no error", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := As(tc.err)
			if got != tc.want || ok != (tc.want != nil) {
				t.Fatalf("as = %v, %v; want %v", got, ok, tc.want)
			}
		})
	}
}
