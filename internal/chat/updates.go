package chat

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"tenzo-run-backend/internal/shared/httpx"
	"tenzo-run-backend/internal/shared/ratelimit"
)

type UpdateMessage struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type UpdatesBody struct {
	Messages []UpdateMessage `json:"messages"`
	Total    int             `json:"total" doc:"Answers from Sanzhar in the chat so far, pass it as after next time"`
	Waiting  bool            `json:"waiting" doc:"The consultant is waiting for an answer from Sanzhar"`
}

type updatesInput struct {
	ID    string `path:"id" format:"uuid"`
	After int    `query:"after" minimum:"0" doc:"How many answers from Sanzhar the client has already seen"`
}

type updatesOutput struct {
	Body UpdatesBody
}

type UpdatesHandler struct {
	svc     *Service
	limiter *ratelimit.Limiter
}

func NewUpdatesHandler(svc *Service, limiter *ratelimit.Limiter) *UpdatesHandler {
	return &UpdatesHandler{svc: svc, limiter: limiter}
}

func (h *UpdatesHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "chat-updates",
		Method:      http.MethodGet,
		Path:        Path + "/{id}/updates",
		Summary:     "Answers from Sanzhar that arrived after the consultant reply",
		Tags:        []string{"Chat"},
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable},
	}, h.updates)
}

func (h *UpdatesHandler) updates(ctx context.Context, in *updatesInput) (*updatesOutput, error) {
	if h.svc == nil {
		return nil, huma.Error503ServiceUnavailable("chat is disabled")
	}
	if ok, wait := h.limiter.Allow(httpx.RateKey(httpx.ClientIP(ctx))); !ok {
		return nil, httpx.TooManyRequests(wait)
	}
	id, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid chat id")
	}
	u, err := h.svc.Updates(ctx, id, in.After)
	if err != nil {
		return nil, httpx.Error(ctx, err)
	}
	out := UpdatesBody{Messages: make([]UpdateMessage, 0, len(u.Messages)), Total: u.Total, Waiting: u.Waiting}
	for _, t := range u.Messages {
		out.Messages = append(out.Messages, UpdateMessage{Text: t.Text, At: t.At})
	}
	return &updatesOutput{Body: out}, nil
}
