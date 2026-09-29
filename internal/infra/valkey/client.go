package valkey

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"

	"github.com/Everfit-io/go-service-template/internal/platform/config"
)

// Client wraps *redis.Client with the package identity "valkey" to reflect the
// actual server software while remaining wire-compatible with go-redis.
type Client struct {
	*redis.Client
}

// Connect dials Valkey and retries the ping up to 3 times (delays 1s, 2s, 4s).
func Connect(ctx context.Context, cfg config.ValkeyConfig, log *slog.Logger) (*Client, error) {
	rc := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	// OTel: driver-level hooks emit a span + metric per Redis/Valkey command.
	// No-op when no exporter is configured. Failures during instrument setup
	// are unexpected (version mis-pin); log and continue rather than fail boot.
	if err := redisotel.InstrumentTracing(rc); err != nil {
		log.Warn("valkey: otel tracing instrumentation skipped", slog.String("error", err.Error()))
	}
	if err := redisotel.InstrumentMetrics(rc); err != nil {
		log.Warn("valkey: otel metrics instrumentation skipped", slog.String("error", err.Error()))
	}

	backoff := time.Second
	var lastErr error
	for i := 1; i <= 3; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = rc.Ping(pingCtx).Err()
		cancel()
		if lastErr == nil {
			log.Info("valkey connected", slog.String("phase", "startup"), slog.Int("attempt", i))
			return &Client{rc}, nil
		}
		log.Warn("valkey ping failed",
			slog.String("phase", "startup"),
			slog.Int("attempt", i),
			slog.String("error", lastErr.Error()),
		)
		if i == 3 {
			break
		}
		select {
		case <-ctx.Done():
			if cErr := rc.Close(); cErr != nil {
				log.Warn("valkey close after ctx cancel", slog.String("error", cErr.Error()))
			}
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	if cErr := rc.Close(); cErr != nil {
		log.Warn("valkey close after ping exhaustion", slog.String("error", cErr.Error()))
	}
	return nil, fmt.Errorf("valkey: ping exhausted after 3 attempts: %w", lastErr)
}

// Ping checks whether Valkey is reachable within the context deadline.
func (c *Client) Ping(ctx context.Context) error {
	return c.Client.Ping(ctx).Err()
}

// Close shuts down the underlying redis connection pool.
func (c *Client) Close() error {
	return c.Client.Close()
}
