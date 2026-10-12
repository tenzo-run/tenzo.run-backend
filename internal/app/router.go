package app

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tenzo-run-backend/internal/config"
	"tenzo-run-backend/internal/lead"
	"tenzo-run-backend/internal/shared/httpx"
	"tenzo-run-backend/internal/shared/postgres"
)

var allowedHeaders = []string{
	"Content-Type",
	httpx.RequestIDHeader,
}

func newRouter(cfg config.Config, log *slog.Logger, pool *pgxpool.Pool, svc *services) (http.Handler, error) {
	resolver, err := httpx.NewIPResolver(cfg.HTTP.TrustedProxies)
	if err != nil {
		return nil, err
	}

	crossOrigin, err := httpx.CrossOriginProtection(cfg.HTTP.AllowedOrigins)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(
		httpx.RequestID,
		resolver.Middleware,
		httpx.AccessLog(log),
		httpx.Recover,
		httpx.SecurityHeaders(strings.HasPrefix(cfg.HTTP.PublicURL, "https://")),
		httpx.CORS(cfg.HTTP.AllowedOrigins, allowedHeaders),
		crossOrigin,
	)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := postgres.Ping(r.Context(), pool); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	api := humachi.New(r, humaConfig(cfg))
	registerRoutes(api, svc)

	return r, nil
}

func registerRoutes(api huma.API, svc *services) {
	v1 := huma.NewGroup(api, "/api/v1")
	lead.NewHandler(svc.leads, svc.leadLimiter).Register(v1)
}

func humaConfig(cfg config.Config) huma.Config {
	c := huma.DefaultConfig("tenzo.run API", Version)
	c.CreateHooks = nil
	c.Servers = []*huma.Server{{URL: cfg.HTTP.PublicURL}}

	if !cfg.HTTP.Docs {
		c.DocsPath = ""
		c.OpenAPIPath = ""
		c.SchemasPath = ""
	}

	return c
}
