package chat

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/shared/apperr"
	"tenzo-run-backend/internal/shared/httpx"
	"tenzo-run-backend/internal/shared/logger"
	"tenzo-run-backend/internal/shared/ratelimit"
)

const Path = "/chat"

type request struct {
	Session string `json:"session"`
	Text    string `json:"text"`
	Locale  string `json:"locale"`
	Page    string `json:"page"`
	Website string `json:"website"`
}

type Handler struct {
	svc          *Service
	limiter      *ratelimit.Limiter
	maxBodyBytes int64
	timeout      time.Duration
}

func NewHandler(svc *Service, limiter *ratelimit.Limiter, maxBodyBytes int64, timeout time.Duration) *Handler {
	return &Handler{svc: svc, limiter: limiter, maxBodyBytes: maxBodyBytes, timeout: timeout}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if h.svc == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable)
		return
	}
	if ok, wait := h.limiter.Allow(httpx.RateKey(httpx.ClientIP(ctx))); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(wait.Round(time.Second)/time.Second))))
		httpx.WriteProblem(w, http.StatusTooManyRequests)
		return
	}

	var req request
	if err := json.NewDecoder(io.LimitReader(r.Body, h.maxBodyBytes)).Decode(&req); err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest)
		return
	}

	in := Input{Text: req.Text, Locale: req.Locale, Page: req.Page}
	if req.Session != "" {
		id, err := uuid.Parse(req.Session)
		if err != nil {
			httpx.WriteProblem(w, http.StatusBadRequest)
			return
		}
		in.Session = id
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(h.timeout))

	s := &stream{w: w, rc: rc}
	if req.Website != "" {
		s.send("done", struct{}{})
		return
	}

	err := h.svc.Reply(ctx, in, s)
	switch {
	case err == nil:
		s.send("done", struct{}{})
	case !s.started:
		if e, ok := apperr.As(err); ok {
			httpx.WriteProblem(w, httpx.Status(e.Kind))
			return
		}
		logger.From(ctx).ErrorContext(ctx, "chat failed", slog.Any("error", err))
		httpx.WriteProblem(w, http.StatusBadGateway)
	default:
		if !errors.Is(err, ctx.Err()) {
			logger.From(ctx).ErrorContext(ctx, "chat reply failed", slog.Any("error", err))
		}
		s.send("error", map[string]string{"code": "unavailable"})
	}
}

type stream struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	started bool
}

func (s *stream) Session(id uuid.UUID) {
	s.send("session", map[string]string{"id": id.String()})
}

func (s *stream) Delta(text string) {
	s.send("delta", map[string]string{"text": text})
}

func (s *stream) Lead(l Lead) {
	s.send("lead", map[string]bool{"contact": l.Contact != "", "name": l.Name != ""})
}

func (s *stream) send(event string, data any) {
	if !s.started {
		h := s.w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}

	payload, _ := json.Marshal(data)
	_, _ = io.WriteString(s.w, "event: "+event+"\ndata: "+string(payload)+"\n\n")
	_ = s.rc.Flush()
}
