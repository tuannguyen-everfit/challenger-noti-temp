package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Everfit-io/go-service-template/internal/features/notification"
	"github.com/Everfit-io/go-service-template/internal/infra/challengerclient"
	"github.com/Everfit-io/go-service-template/internal/infra/kafka"
	"github.com/Everfit-io/go-service-template/internal/infra/mongox"
	"github.com/Everfit-io/go-service-template/internal/infra/otelx"
	"github.com/Everfit-io/go-service-template/internal/infra/valkey"
	"github.com/Everfit-io/go-service-template/internal/platform/authtoken"
	"github.com/Everfit-io/go-service-template/internal/platform/buildinfo"
	"github.com/Everfit-io/go-service-template/internal/platform/config"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/health"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/middleware"
	"github.com/Everfit-io/go-service-template/internal/stdx/safego"
)

// Run executes the full bootstrap sequence and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log.Info("bootstrap starting", slog.String("phase", "startup"))

	otelShutdown, err := otelx.Setup(ctx, cfg.Env, buildinfo.Version(), log)
	if err != nil {
		return fmt.Errorf("app: otel: %w", err)
	}

	mongoClient, err := mongox.Connect(ctx, cfg.Mongo, log)
	if err != nil {
		return fmt.Errorf("app: mongo: %w", err)
	}
	log.Info("mongo ready", slog.String("phase", "startup"))

	if err := mongoClient.EnsureIndexes(ctx,
		notification.NewIndexEnsurer(mongoClient),
	); err != nil {
		return fmt.Errorf("app: ensure indexes: %w", err)
	}

	valkeyClient, err := valkey.Connect(ctx, cfg.Valkey, log)
	if err != nil {
		return fmt.Errorf("app: valkey: %w", err)
	}
	log.Info("valkey ready", slog.String("phase", "startup"))

	// Kafka producer — opt-in via SVC_KAFKA_BROKERS.
	var producer kafka.Producer
	if len(cfg.Kafka.Brokers) > 0 {
		producer, err = kafka.NewProducer(kafka.ProducerConfig{Brokers: cfg.Kafka.Brokers}, log)
		if err != nil {
			return fmt.Errorf("app: kafka producer: %w", err)
		}
		log.Info("kafka producer ready",
			slog.String("phase", "startup"),
			slog.Int("brokers", len(cfg.Kafka.Brokers)),
		)
	}

	accessVerifier, err := newAccessVerifier(cfg.Auth)
	if err != nil {
		return err
	}

	// Lazy dial: boot never waits on challenger; the first mark-read connects.
	challengerClient, err := challengerclient.New(ctx, challengerclient.Config{
		Addr:        cfg.Challenger.GRPCAddr,
		Secret:      cfg.Challenger.GRPCSecret,
		CallTimeout: cfg.Challenger.GRPCCallTimeout,
		MaxAttempts: cfg.Challenger.GRPCMaxAttempts,
	})
	if err != nil {
		return fmt.Errorf("app: challenger client: %w", err)
	}

	notificationSvc := notification.New(
		notification.NewMongoRepository(mongoClient),
		notification.NewMongoAuditWriter(mongoClient),
		challengerClient,
		notification.Config{MaxListLimit: cfg.Pagination.MaxLimit},
	)
	notificationHandler := notification.NewHandler(notificationSvc)

	healthHandler := health.New(mongoClient, valkeyClient, health.Meta{
		Region:  cfg.AppRegion,
		Name:    cfg.AppName,
		Env:     cfg.Env,
		Version: buildinfo.Version(),
	})

	router := httpx.NewRouter(httpx.RouterDeps{
		Health:                     healthHandler,
		Notification:               notificationHandler,
		AccessVerifier:             accessVerifier,
		IdempotencyCache:           newValkeyIdempotencyCache(valkeyClient),
		NotificationInternalSecret: cfg.Notification.InternalAPISecret,
		HTTPTimeout:                cfg.HTTPTimeout,
		ThrottleMax:                cfg.HTTPThrottle.Max,
		ThrottleBacklog:            cfg.HTTPThrottle.Backlog,
		ThrottleBacklogTimeout:     cfg.HTTPThrottle.BacklogTimeout,
		PayloadCapture: middleware.PayloadCapture{
			Bodies:      cfg.HTTPLog.Bodies,
			Query:       cfg.HTTPLog.Query,
			QueryDeny:   cfg.HTTPLog.QueryDeny,
			MaxValueLen: cfg.HTTPLog.MaxValueLen,
			MaxFields:   cfg.HTTPLog.MaxFields,
			Headers:     cfg.HTTPLog.Headers,
		},
	})

	httpAddr := fmt.Sprintf(":%d", cfg.HTTPPort)
	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	srvErr := make(chan error, 1)
	safego.Go(func() {
		log.Info("http server listening",
			slog.String("phase", "startup"),
			slog.String("addr", httpAddr),
			slog.Duration("request_timeout", cfg.HTTPTimeout),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	})

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-srvErr:
		return fmt.Errorf("app: http server: %w", err)
	}

	return shutdown(ctx, cfg.ShutdownTimeout, healthHandler, srv, producer, challengerClient, valkeyClient, mongoClient, otelShutdown, log)
}

// newAccessVerifier builds the JWT signer that backs middleware.BearerAuth.
// Both secrets are required (config.validate enforces it) because every public
// route is Bearer-authed.
func newAccessVerifier(cfg config.AuthConfig) (middleware.AccessVerifier, error) {
	signer, err := authtoken.New(authtoken.Config{
		AccessSecret:  cfg.JWTAccessSecret,
		RefreshSecret: cfg.JWTRefreshSecret,
		AccessTTL:     cfg.JWTAccessTTL,
		RefreshTTL:    cfg.JWTRefreshTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("app: jwt signer: %w", err)
	}
	middleware.SetExpiredSentinel(authtoken.ErrExpired)
	return signer, nil
}

func shutdown(
	parent context.Context,
	timeout time.Duration,
	healthHandler *health.Handler,
	srv *http.Server,
	producer kafka.Producer,
	challengerClient *challengerclient.Client,
	valkeyClient *valkey.Client,
	mongoClient *mongox.Client,
	otelShutdown otelx.ShutdownFunc,
	log *slog.Logger,
) error {
	// 1. Signal liveness probe — k8s pulls pod from Service before close.
	healthHandler.Shutdown()

	// 2. Overall deadline that won't inherit ctx cancellation from SIGTERM.
	// Sub-budgets below carve this up so the HTTP drain can't starve the
	// cleanup tail (OTel flush + Mongo close need ctx; an exhausted shutCtx
	// makes them return immediately without flushing).
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()

	// 3. Stop accepting new HTTP requests + drain in-flight, capped at 80%
	// of total. Reserves 20% for the cleanup tail (OTel + Mongo).
	httpCtx, httpCancel := context.WithTimeout(shutCtx, timeout*4/5)
	log.Info("shutting down http server")
	if err := srv.Shutdown(httpCtx); err != nil {
		log.Error("http shutdown error", slog.String("error", err.Error()))
	}
	httpCancel()

	// 4. Kafka consumers: cancel + Close each one here, BEFORE the producer
	// (kafka.md §5). None are wired in the template.

	// 5. Flush kafka producer if it was started.
	if producer != nil {
		log.Info("closing kafka producer")
		if err := producer.Close(); err != nil {
			log.Warn("kafka producer close error", slog.String("error", err.Error()))
		}
	}

	// 6. Outbound clients (gRPC conns) close here, before the OTel flush — a
	// client may emit a final span on Close.
	log.Info("closing challenger grpc client")
	if err := challengerClient.Close(); err != nil {
		log.Warn("challenger grpc client close error", slog.String("error", err.Error()))
	}

	// 7. Flush OTel exporter so the last spans land in the collector.
	log.Info("shutting down otel")
	if err := otelShutdown(shutCtx); err != nil {
		log.Warn("otel shutdown error", slog.String("error", err.Error()))
	}

	// 8. Cache before DB — cache writes are non-durable, lose them last.
	log.Info("closing valkey")
	if err := valkeyClient.Close(); err != nil {
		log.Warn("valkey close error", slog.String("error", err.Error()))
	}

	// 9. DB last — readers above might still be finishing.
	log.Info("disconnecting mongo")
	if err := mongoClient.Close(shutCtx); err != nil {
		log.Warn("mongo close error", slog.String("error", err.Error()))
	}

	log.Info("shutdown complete")
	return nil
}

// valkeyIdempotencyCache adapts *valkey.Client to middleware.IdempotencyCache.
// Lives here (composition root) so router.go stays driver-agnostic and tests
// can inject a mock cache via RouterDeps.IdempotencyCache.
type valkeyIdempotencyCache struct{ client *valkey.Client }

func newValkeyIdempotencyCache(v *valkey.Client) *valkeyIdempotencyCache {
	return &valkeyIdempotencyCache{client: v}
}

func (c *valkeyIdempotencyCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	raw, err := c.client.Get(ctx, key).Bytes()
	if err == nil {
		return raw, true, nil
	}
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return nil, false, err
}

func (c *valkeyIdempotencyCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return c.client.Set(ctx, key, val, ttl).Err()
}
