package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"tenzo-run-backend/internal/shared/apperr"
	"tenzo-run-backend/internal/shared/logger"
)

func Error(ctx context.Context, err error) error {
	if e, ok := apperr.As(err); ok {
		return huma.NewError(Status(e.Kind), e.Message)
	}

	if errors.Is(err, context.Canceled) {
		logger.From(ctx).DebugContext(ctx, "request canceled", slog.Any("error", err))
		return huma.NewError(http.StatusServiceUnavailable, "request canceled")
	}

	logger.From(ctx).ErrorContext(ctx, "request failed", slog.Any("error", err))
	return huma.Error500InternalServerError("internal server error")
}

func TooManyRequests(retryAfter time.Duration) error {
	seconds := max(1, int(retryAfter.Round(time.Second)/time.Second))
	return huma.ErrorWithHeaders(
		huma.Error429TooManyRequests("too many requests, try again later"),
		http.Header{"Retry-After": {strconv.Itoa(seconds)}},
	)
}

func Status(kind apperr.Kind) int {
	switch kind {
	case apperr.KindInvalid:
		return http.StatusUnprocessableEntity
	case apperr.KindUnauthorized:
		return http.StatusUnauthorized
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindTooManyRequests:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}
