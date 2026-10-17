package lead

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/danielgtaylor/huma/v2"

	"tenzo-run-backend/internal/shared/httpx"
	"tenzo-run-backend/internal/shared/ratelimit"
)

const (
	tag             = "Leads"
	maxRequestBytes = 16 << 10
)

type RequestBody struct {
	Name    string `json:"name" minLength:"1" maxLength:"100" doc:"How to address the client"`
	Contact string `json:"contact" minLength:"3" maxLength:"200" doc:"Telegram handle, WhatsApp or phone number, or email"`
	Locale  Locale `json:"locale" enum:"ru,en,zh" doc:"Language of the page the request was sent from"`
	Page    string `json:"page,omitempty" maxLength:"300" required:"false" doc:"Path of the page with the form"`
	Message string `json:"message,omitempty" maxLength:"2000" required:"false" doc:"What the client wants to build"`
	Website string `json:"website,omitempty" maxLength:"200" required:"false" doc:"Must stay empty"`
}

func (b RequestBody) draft(ip netip.Addr) Draft {
	return Draft{
		Name:    b.Name,
		Contact: b.Contact,
		Locale:  b.Locale,
		Page:    b.Page,
		Message: b.Message,
		Website: b.Website,
		IP:      ip,
	}
}

type CreatedBody struct {
	ID string `json:"id" format:"uuid"`
}

type createInput struct {
	Body RequestBody
}

type createOutput struct {
	Body CreatedBody
}

type Handler struct {
	svc     *Service
	limiter *ratelimit.Limiter
}

func NewHandler(svc *Service, limiter *ratelimit.Limiter) *Handler {
	return &Handler{svc: svc, limiter: limiter}
}

func (h *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-lead",
		Method:        http.MethodPost,
		Path:          "/leads",
		Summary:       "Send a project request from the site",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
		MaxBodyBytes:  maxRequestBytes,
		Errors:        []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}, h.create)
}

func (h *Handler) create(ctx context.Context, in *createInput) (*createOutput, error) {
	ip := httpx.ClientIP(ctx)
	if ok, wait := h.limiter.Allow(httpx.RateKey(ip)); !ok {
		return nil, httpx.TooManyRequests(wait)
	}

	id, err := h.svc.Submit(ctx, in.Body.draft(ip))
	if err != nil {
		return nil, httpx.Error(ctx, err)
	}
	return &createOutput{Body: CreatedBody{ID: id.String()}}, nil
}
