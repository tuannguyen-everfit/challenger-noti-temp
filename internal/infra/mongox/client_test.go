package mongox

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// newOfflineClient returns a Client that never reaches a server: a transaction with no
// operations starts, commits and aborts client-side only.
func newOfflineClient(t *testing.T) *Client {
	t.Helper()
	mc, err := mongo.Connect(options.Client().ApplyURI("mongodb://127.0.0.1:1/?replicaSet=rs0"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := mc.Disconnect(context.Background()); err != nil {
			t.Errorf("disconnect: %v", err)
		}
	})
	return &Client{Client: mc, DB: mc.Database("offline")}
}

func TestWithTransaction_RunsFnWithSessionContext(t *testing.T) {
	c := newOfflineClient(t)
	ran := false
	err := c.WithTransaction(context.Background(), func(ctx context.Context) error {
		ran = true
		if mongo.SessionFromContext(ctx) == nil {
			t.Error("fn ctx carries no session")
		}
		return nil
	})
	if err != nil || !ran {
		t.Errorf("WithTransaction = %v, ran = %v; want nil, true", err, ran)
	}
}

func TestWithTransaction_ReturnsFnError(t *testing.T) {
	c := newOfflineClient(t)
	boom := errors.New("boom")
	if err := c.WithTransaction(context.Background(), func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("WithTransaction = %v, want %v", err, boom)
	}
}
