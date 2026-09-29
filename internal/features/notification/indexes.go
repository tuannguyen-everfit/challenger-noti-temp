package notification

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/Everfit-io/go-service-template/internal/infra/mongox"
)

const (
	collectionName = "notifications"
	// retention is a const, not an env: changing it needs a manual collMod (see
	// docs/features/notification/solution-design.md) or every pod crashloops on
	// IndexOptionsConflict.
	retention = 180 * 24 * time.Hour
)

var requiredIndexes = []mongox.IndexSpec{
	{
		Name: "notifications_feed",
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "activity_at", Value: -1}, {Key: "_id", Value: -1}},
	},
	{
		Name: "notifications_feed_tab",
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "tab", Value: 1}, {Key: "activity_at", Value: -1}, {Key: "_id", Value: -1}},
	},
	{
		// Not partial: partialFilterExpression can't express `$exists: false`;
		// summaryCountCap bounds the FETCH cost instead.
		Name: "notifications_unread",
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "tab", Value: 1}, {Key: "read_at", Value: 1}},
	},
	{
		Name: "notifications_ttl",
		Keys: bson.D{{Key: "created_at", Value: 1}},
		TTL:  retention,
	},
}

// NewIndexEnsurer returns the IndexEnsurer for the notifications collection.
func NewIndexEnsurer(m *mongox.Client) mongox.IndexEnsurer {
	return indexEnsurer{coll: m.DB.Collection(collectionName)}
}

type indexEnsurer struct{ coll *mongo.Collection }

func (e indexEnsurer) Ensure(ctx context.Context, _ *mongo.Database) error {
	return mongox.EnsureSpecs(ctx, e.coll, requiredIndexes)
}
