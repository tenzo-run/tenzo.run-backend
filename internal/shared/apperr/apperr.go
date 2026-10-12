package apperr

import "errors"

type Kind uint8

const (
	KindInternal Kind = iota
	KindInvalid
	KindUnauthorized
	KindForbidden
	KindNotFound
	KindConflict
	KindTooManyRequests
)

type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

func Invalid(msg string) *Error {
	return &Error{Kind: KindInvalid, Message: msg}
}

func Unauthorized(msg string) *Error {
	return &Error{Kind: KindUnauthorized, Message: msg}
}

func Forbidden(msg string) *Error {
	return &Error{Kind: KindForbidden, Message: msg}
}

func NotFound(msg string) *Error {
	return &Error{Kind: KindNotFound, Message: msg}
}

func Conflict(msg string) *Error {
	return &Error{Kind: KindConflict, Message: msg}
}

func TooManyRequests(msg string) *Error {
	return &Error{Kind: KindTooManyRequests, Message: msg}
}

func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
