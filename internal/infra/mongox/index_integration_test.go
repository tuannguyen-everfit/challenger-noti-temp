//go:build integration

// Integration tests for EnsureSpecs against a real MongoDB replica set.
// Run via `make compose-up && make test-integration`.
package mongox

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Everfit-io/go-service-template/internal/platform/config"
)

func newTestCollection(t *testing.T) (*Client, string) {
	t.Helper()
	uri := os.Getenv("SVC_MONGO_URI")
	if uri == "" {
		t.Skip("SVC_MONGO_URI not set — run `make compose-up` first")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Connect(ctx, config.MongoConfig{
		URI:         uri,
		Database:    "service_int_test",
		PingTimeout: 5 * time.Second,
	}, log)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	collName := "indextest_" + time.Now().Format("150405.000")
	t.Cleanup(func() {
		_ = c.DB.Collection(collName).Drop(context.Background())
		_ = c.Close(context.Background())
	})
	return c, collName
}

func TestEnsureSpecs_CreatesAndIsIdempotent(t *testing.T) {
	c, collName := newTestCollection(t)
	coll := c.DB.Collection(collName)
	ctx := context.Background()

	specs := []IndexSpec{
		{Name: "by_owner_created", Keys: bson.D{{Key: "owner", Value: 1}, {Key: "created_at", Value: -1}}},
		{Name: "by_email_unique", Keys: bson.D{{Key: "email", Value: 1}}, Unique: true},
	}

	// First call creates both indexes.
	if err := EnsureSpecs(ctx, coll, specs); err != nil {
		t.Fatalf("first EnsureSpecs: %v", err)
	}
	// Second call is a no-op (Mongo's CreateIndexes is idempotent on name+keys).
	if err := EnsureSpecs(ctx, coll, specs); err != nil {
		t.Errorf("second EnsureSpecs should be a no-op, got: %v", err)
	}

	// Verify both indexes exist on the collection.
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	var got []bson.M
	if err := cur.All(ctx, &got); err != nil {
		t.Fatalf("decode indexes: %v", err)
	}
	names := map[string]bool{}
	for _, idx := range got {
		if n, ok := idx["name"].(string); ok {
			names[n] = true
		}
	}
	for _, want := range []string{"by_owner_created", "by_email_unique"} {
		if !names[want] {
			t.Errorf("index %q not created; got %v", want, names)
		}
	}
}

func TestEnsureSpecs_ConflictingKeysSameNameFails(t *testing.T) {
	c, collName := newTestCollection(t)
	coll := c.DB.Collection(collName)
	ctx := context.Background()

	if err := EnsureSpecs(ctx, coll, []IndexSpec{
		{Name: "conflict_test", Keys: bson.D{{Key: "a", Value: 1}}},
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Same name, different keys → Mongo returns IndexKeySpecsConflict.
	err := EnsureSpecs(ctx, coll, []IndexSpec{
		{Name: "conflict_test", Keys: bson.D{{Key: "b", Value: 1}}},
	})
	if err == nil {
		t.Errorf("err = nil, want IndexKeySpecsConflict from Mongo")
	}
}
