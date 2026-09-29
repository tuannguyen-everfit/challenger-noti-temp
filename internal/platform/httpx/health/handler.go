package health

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Everfit-io/go-service-template/internal/platform/httpx/respond"
	"github.com/Everfit-io/go-service-template/internal/stdx/safego"
)

// Pinger is implemented by both the mongo client and the valkey client.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Meta is the static service identity surfaced in the /healthcheck body
// (app_region / app_name / app_env / app_version). Built once at boot from
// config + buildinfo and stored on the Handler.
type Meta struct {
	Region  string
	Name    string
	Env     string
	Version string
}

// Handler serves the three health endpoints and tracks shutdown state via an
// atomic flag (avoids sync.Mutex.TryLock false-503s under concurrent probes).
type Handler struct {
	mongo  Pinger
	valkey Pinger
	meta   Meta

	// shuttingDown is set to true by Shutdown(); /liveness returns 503 once set.
	shuttingDown atomic.Bool
}

// New creates a Handler that checks mongo and valkey for readiness. meta carries
// the static identity fields echoed by /healthcheck.
func New(mongo, valkey Pinger, meta Meta) *Handler {
	return &Handler{mongo: mongo, valkey: valkey, meta: meta}
}

// checkResult is one dependency probe outcome on the /healthcheck wire body.
type checkResult struct {
	Name      string `json:"name"`
	IsHealthy bool   `json:"is_healthy"`
	Message   string `json:"message,omitempty"`
}

// statusResponse is the /healthcheck wire body: static identity + per-dep checks.
type statusResponse struct {
	Status     string        `json:"status"`
	AppRegion  string        `json:"app_region"`
	AppName    string        `json:"app_name"`
	AppEnv     string        `json:"app_env"`
	AppVersion string        `json:"app_version"`
	Checks     []checkResult `json:"checks"`
}

const (
	statusRunning   = "Running"
	statusDegraded  = "Degraded"
	msgConnected    = "Connected"
	msgDisconnected = "Disconnected"
)

// Shutdown is called by the graceful-shutdown sequence. It flips the
// shutdown flag so subsequent /liveness requests return 503, signalling k8s
// to stop routing new traffic before the process exits.
func (h *Handler) Shutdown() {
	h.shuttingDown.Store(true)
}

// Healthz serves /healthcheck — reports service identity plus a live probe of
// each dependency. Returns 200 + status "Running" when all checks pass, 503 +
// status "Degraded" when any dependency is down.
//
// NOTE: per UP-72-healthcheck this endpoint deliberately probes Mongo + Valkey
// and can return 503 — overriding the original "always-cheap 200" design. k8s
// liveness/readiness probes MUST target /liveness and /readiness (NOT this
// endpoint), or a transient dependency blip would 503 here and kill the pod.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	checks := h.runChecks(r.Context())

	status := statusRunning
	code := http.StatusOK
	for _, c := range checks {
		if !c.IsHealthy {
			status = statusDegraded
			code = http.StatusServiceUnavailable
			break
		}
	}

	respond.JSON(w, code, statusResponse{
		Status:     status,
		AppRegion:  h.meta.Region,
		AppName:    h.meta.Name,
		AppEnv:     h.meta.Env,
		AppVersion: h.meta.Version,
		Checks:     checks,
	})
}

// Livez serves /liveness — returns 503 after Shutdown() has been called; otherwise 200.
func (h *Handler) Livez(w http.ResponseWriter, _ *http.Request) {
	if h.shuttingDown.Load() {
		respond.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting_down"})
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz serves /readiness — pings both dependencies with a 1-second timeout each.
// Returns 503 with per-dep status when either is unavailable.
func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	type result struct {
		name string
		err  error
	}

	ping := func(ctx context.Context, name string, p Pinger, ch chan<- result) {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		ch <- result{name: name, err: p.Ping(pingCtx)}
	}

	ctx := r.Context()
	mongoCh := make(chan result, 1)
	valkeyCh := make(chan result, 1)
	safego.Go(func() { ping(ctx, "mongo", h.mongo, mongoCh) })
	safego.Go(func() { ping(ctx, "valkey", h.valkey, valkeyCh) })

	mr := <-mongoCh
	vr := <-valkeyCh

	mongoStatus := "ok"
	valkeyStatus := "ok"
	code := http.StatusOK

	if mr.err != nil {
		mongoStatus = "down"
		code = http.StatusServiceUnavailable
	}
	if vr.err != nil {
		valkeyStatus = "down"
		code = http.StatusServiceUnavailable
	}

	respond.JSON(w, code, map[string]string{
		"mongo":  mongoStatus,
		"valkey": valkeyStatus,
	})
}

// runChecks probes mongo + valkey concurrently (1s timeout each) and returns the
// results in a stable order (mongodb, then redis). Wire labels use the canonical
// product names (mongodb / redis), not the internal driver names.
func (h *Handler) runChecks(ctx context.Context) []checkResult {
	mongoCh := make(chan checkResult, 1)
	redisCh := make(chan checkResult, 1)
	safego.Go(func() { mongoCh <- probe(ctx, "mongodb", h.mongo) })
	safego.Go(func() { redisCh <- probe(ctx, "redis", h.valkey) })
	return []checkResult{<-mongoCh, <-redisCh}
}

// probe pings one dependency with a 1-second timeout and maps the outcome to a
// checkResult. The message is a static label — raw driver errors are not leaked
// to clients (see .claude/rules/http.md).
func probe(ctx context.Context, name string, p Pinger) checkResult {
	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := p.Ping(pingCtx); err != nil {
		return checkResult{Name: name, IsHealthy: false, Message: msgDisconnected}
	}
	return checkResult{Name: name, IsHealthy: true, Message: msgConnected}
}
