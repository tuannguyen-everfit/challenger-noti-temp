package mongox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.opentelemetry.io/contrib/instrumentation/go.mongodb.org/mongo-driver/v2/mongo/otelmongo"

	"github.com/Everfit-io/go-service-template/internal/platform/config"
)

// IndexEnsurer is implemented by any domain package that owns a collection's
// index definitions. EnsureIndexes calls each in order at startup (B-03).
type IndexEnsurer interface {
	Ensure(ctx context.Context, db *mongo.Database) error
}

// Client wraps the official mongo client and exposes the target database.
type Client struct {
	*mongo.Client
	DB *mongo.Database
}

// Connect dials MongoDB and retries the ping with exponential backoff
// (5 attempts, delays 1s, 2s, 4s, 8s, 16s). Returns an error on exhaustion.
func Connect(ctx context.Context, cfg config.MongoConfig, log *slog.Logger) (*Client, error) {
	opts := options.Client().
		ApplyURI(cfg.URI).
		SetServerSelectionTimeout(5 * time.Second).
		SetAppName("go-service-template").
		// OTel: driver-level CommandMonitor emits a span per Mongo command
		// (e.g. service.items.find). No-op when no exporter is configured.
		// No business-code changes required.
		SetMonitor(otelmongo.NewMonitor())

	mc, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("mongox: connect: %w", err)
	}

	backoff := time.Second
	for i := 1; i <= 5; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
		err = mc.Ping(pingCtx, nil)
		cancel()
		if err == nil {
			log.Info("mongo connected", slog.String("phase", "startup"), slog.Int("attempt", i))
			break
		}
		log.Warn("mongo ping failed",
			slog.String("phase", "startup"),
			slog.Int("attempt", i),
			slog.String("error", err.Error()),
		)
		if i == 5 {
			disconnect(ctx, mc, log, "after ping failure")
			return nil, fmt.Errorf("mongox: ping exhausted after 5 attempts: %w", err)
		}
		select {
		case <-ctx.Done():
			disconnect(ctx, mc, log, "after ctx cancel")
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	return &Client{
		Client: mc,
		DB:     mc.Database(cfg.Database),
	}, nil
}

// Ping checks whether MongoDB is reachable within the context deadline.
func (c *Client) Ping(ctx context.Context) error {
	return c.Client.Ping(ctx, nil)
}

// WithTransaction runs fn in one multi-document transaction (replica set
// required). Every call made with fn's ctx commits or rolls back together; the
// driver re-runs fn on TransientTransactionError, so fn must be re-runnable.
func (c *Client) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	sess, err := c.StartSession()
	if err != nil {
		return fmt.Errorf("mongox: start session: %w", err)
	}
	defer sess.EndSession(context.WithoutCancel(ctx))
	_, err = sess.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		return nil, fn(txCtx)
	})
	return err
}

// Close disconnects the underlying mongo client.
func (c *Client) Close(ctx context.Context) error {
	return c.Disconnect(ctx)
}

// disconnect runs Disconnect with a fresh deadline derived from ctx's values
// (but not its cancellation) so cleanup can finish after the parent is done.
func disconnect(ctx context.Context, mc *mongo.Client, log *slog.Logger, when string) {
	dCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := mc.Disconnect(dCtx); err != nil {
		log.Warn("mongo disconnect "+when, slog.String("error", err.Error()))
	}
}

// EnsureIndexes runs each IndexEnsurer against the configured database.
// Scaffold registers zero ensurers; domain packages add them in B-03.
func (c *Client) EnsureIndexes(ctx context.Context, ensurers ...IndexEnsurer) error {
	for _, e := range ensurers {
		if err := e.Ensure(ctx, c.DB); err != nil {
			return fmt.Errorf("mongox: ensure indexes: %w", err)
		}
	}
	return nil
}
