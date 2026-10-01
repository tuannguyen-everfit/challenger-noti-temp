package httpx

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/Everfit-io/go-service-template/internal/features/notification"
	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/health"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/middleware"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/respond"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// idempotencyCacheTTL is the replay window for Idempotency-Key responses.
const idempotencyCacheTTL = 24 * time.Hour

// apiVersionPrefix is the single source of truth for the versioned API base
// path. Every feature mounts under this prefix via the v1 group in NewRouter —
// no feature should hardcode `/api/v1/...` in its own Routes(). A future v2
// adds another `r.Route(...)` block alongside it; features migrate by being
// re-mounted in the new group, not by editing their own paths.
const apiVersionPrefix = "/api/v1"

// RouterDeps groups everything NewRouter needs. Passing a struct (rather than
// growing the positional parameter list) keeps the signature stable as we add
// features and platform pieces.
type RouterDeps struct {
	Health       *health.Handler
	Notification *notification.Handler
	// AccessVerifier verifies Bearer access tokens for the authed feature
	// group. Required: app.go fails boot without JWT secrets.
	AccessVerifier   middleware.AccessVerifier
	IdempotencyCache middleware.IdempotencyCache // app.go wires the Valkey-backed adapter
	HTTPTimeout      time.Duration               // global default request timeout; 0 = no timeout

	// PayloadCapture opts the access log into recording request/response
	// payloads. Zero value = capture nothing, which is the default in every
	// environment. See config.HTTPLogConfig before turning any of it on.
	PayloadCapture middleware.PayloadCapture

	// Throttle bounds concurrent in-flight requests through the versioned API
	// group. Health endpoints (/healthcheck, /liveness, /readiness) are NOT
	// throttled — kubelet probes shouldn't see 503 when the app layer is hot.
	// ThrottleMax=0 disables the middleware (NOT recommended for prod).
	ThrottleMax            int
	ThrottleBacklog        int
	ThrottleBacklogTimeout time.Duration
}

// NewRouter builds the chi mux: middleware chain + health probes + a single
// versioned API group that every feature mounts under.
//
// Observability strategy: `otelhttp` ALWAYS wraps the mux. When OTel is
// configured (OTEL_EXPORTER_OTLP_ENDPOINT set, see otelx.Setup) it emits BOTH
// spans AND `http.server.*` metrics. When OTel is unset, the global noop
// providers make otelhttp a near-zero-overhead pass-through. No Prometheus
// middleware or `/metrics` endpoint — OTLP-only.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	// Global middleware — runs for EVERY request including health probes.
	//   1. RequestID  — must run first; stamps logger + echoes header
	//   2. Recover    — catches panics from everything downstream
	//   3. Timeout    — bounds each request; downstream uses ctx-with-deadline
	//   4. AccessLog  — captures the final status code
	//
	// Throttle is scoped to the versioned API group and Idempotency to its authed
	// group — neither belongs on health probes (idempotency replay would mask a
	// real probe failure; throttle would kill kubelet visibility under load).
	r.Use(middleware.RequestID)
	r.Use(middleware.ClientIP) // RFC 7239 + X-Forwarded-For → stamps client_ip on the request logger
	r.Use(middleware.Recover)
	r.Use(middleware.Timeout(deps.HTTPTimeout))
	r.Use(middleware.AccessLog(deps.PayloadCapture))

	// chi 404 / 405 fallbacks — without these, chi emits plain-text bodies
	// that break the {code, message, details} envelope every other endpoint
	// uses.
	//
	// chi quirk: when you override MethodNotAllowed, chi does NOT pre-populate
	// the Allow header (the default handler does, but our override replaces
	// it entirely — chi v5 has no public API to query allowed methods per
	// path). For handler-driven 405s, use apperr.MethodNotAllowed(code, msg,
	// allowed...) — that constructor populates Allow correctly. For chi-
	// generated 405s (wrong verb on a known route), Allow is omitted today.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		respond.MapError(req.Context(), w, apperr.NotFound(localization.CodeRouteNotFound, "route not found"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		respond.MapError(req.Context(), w, apperr.MethodNotAllowed(localization.CodeMethodNotAllowed, "method not allowed for this route"))
	})

	// Health probes — unversioned by kubernetes convention; kubelet doesn't
	// know about API versions, and a probe that 404s during a v1→v2 cutover
	// would kill every pod.
	r.Get("/healthcheck", deps.Health.Healthz)
	r.Get("/liveness", deps.Health.Livez)
	r.Get("/readiness", deps.Health.Readyz)

	// Versioned API group — the ONLY place feature mounts live. Adding a new
	// feature means ONE r.Mount line inside this block. Bumping to v2 means a
	// new r.Route block alongside this one. Feature handlers must NOT prefix
	// their paths with `/api/v1/` themselves.
	r.Route(apiVersionPrefix, func(r chi.Router) {
		r.Use(middleware.Throttle(deps.ThrottleMax, deps.ThrottleBacklog, deps.ThrottleBacklogTimeout))

		// Public feature mounts go here: r.Mount("/<feature>", deps.<Feature>.Routes()).
		// They get no Idempotency: its replay key is scoped by the authenticated caller.

		// Bearer-authed features — the middleware reads the JWT `sub` claim and
		// stamps it on ctx (middleware.UserIDFromContext). A nil AccessVerifier
		// fails closed (500) inside BearerAuth.
		r.Group(func(r chi.Router) {
			r.Use(middleware.BearerAuth(deps.AccessVerifier))
			// After BearerAuth: the replay cache key includes the caller.
			r.Use(middleware.Idempotency(deps.IdempotencyCache, idempotencyCacheTTL))
			r.Mount("/notifications", deps.Notification.Routes())
			r.Mount("/devices", deps.Notification.DeviceRoutes())
		})
	})

	// otelhttp at the OUTERMOST layer so it captures total latency and pulls
	// the chi RoutePattern for span/metric labels (bounded cardinality).
	// Cheap when OTel is disabled (global noop providers).
	//
	// The span-name formatter initially returns `<METHOD> <path>` so traces
	// are usefully labeled even if the request 404s before chi resolves a
	// pattern. middleware.AccessLog (which runs AFTER chi route resolution)
	// renames the span to the route pattern (e.g. `POST /api/v1/<feature>/{id}`)
	// to keep span/metric cardinality bounded. See logger.go.
	return otelhttp.NewHandler(r, "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}
