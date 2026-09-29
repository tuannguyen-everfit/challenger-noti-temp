//go:build integration

package valkey

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/Everfit-io/go-service-template/internal/platform/config"
)

func TestConnect_RealValkey_PingSetGetClose(t *testing.T) {
	addr := os.Getenv("SVC_VALKEY_ADDR")
	if addr == "" {
		t.Skip("SVC_VALKEY_ADDR not set — run `make compose-up` first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Connect(ctx, config.ValkeyConfig{Addr: addr}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := "valkey_int_test:" + time.Now().Format("150405.000000")
	if err := c.Set(ctx, key, "v", time.Minute).Err(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Del(context.Background(), key).Err(); err != nil {
			t.Errorf("Del: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	got, err := c.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v" {
		t.Errorf("Get = %q, want v", got)
	}
}

func TestConnect_Unreachable_ReturnsCtxErrDuringBackoff(t *testing.T) {
	// Port 1 refuses immediately, so the first ping fails fast and Connect
	// parks in the 1s backoff — where the 1.5s deadline fires.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	c, err := Connect(ctx, config.ValkeyConfig{Addr: "localhost:1"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if c != nil {
		t.Errorf("client = %v, want nil", c)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
