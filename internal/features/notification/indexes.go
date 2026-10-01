package notification

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/Everfit-io/go-service-template/internal/infra/mongox"
)

const (
	collectionName       = "notifications"
	deviceCollectionName = "notification_devices"
	auditCollectionName  = "notification_audit_logs"
	// retention is a const, not an env: changing it needs a manual collMod (see
	// docs/features/notification/solution-design.md) or every pod crashloops on
	// IndexOptionsConflict.
	retention = 180 * 24 * time.Hour
	// auditRetention must stay ≥ retention so an entry never expires before the data it explains.
	auditRetention = retention
)

type collectionIndexes struct {
	collection string
	specs      []mongox.IndexSpec
}

var requiredIndexes = []collectionIndexes{
	{collection: collectionName, specs: []mongox.IndexSpec{
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
	}},
	{collection: deviceCollectionName, specs: []mongox.IndexSpec{
		{
			Name:   "devices_user_device",
			Keys:   bson.D{{Key: "user_id", Value: 1}, {Key: "device_id", Value: 1}},
			Unique: true,
		},
		{
			// A token that moves to another account evicts the old row (RegisterDevice).
			Name:   "devices_token",
			Keys:   bson.D{{Key: "token", Value: 1}},
			Unique: true,
		},
	}},
	{collection: auditCollectionName, specs: []mongox.IndexSpec{
		{
			Name: "audit_entity",
			Keys: bson.D{{Key: "entity", Value: 1}, {Key: "entity_id", Value: 1}, {Key: "at", Value: -1}},
		},
		{
			Name:    "audit_user",
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "at", Value: -1}},
			Partial: bson.M{"user_id": bson.M{"$exists": true}},
		},
		{
			Name:    "audit_dedupe",
			Keys:    bson.D{{Key: "audit_key", Value: 1}},
			Unique:  true,
			Partial: bson.M{"audit_key": bson.M{"$exists": true}},
		},
		{
			Name: "audit_ttl",
			Keys: bson.D{{Key: "at", Value: 1}},
			TTL:  auditRetention,
		},
	}},
}

// NewIndexEnsurer returns the IndexEnsurer for the feature's three collections.
func NewIndexEnsurer(m *mongox.Client) mongox.IndexEnsurer {
	return indexEnsurer{db: m.DB}
}

type indexEnsurer struct{ db *mongo.Database }

func (e indexEnsurer) Ensure(ctx context.Context, _ *mongo.Database) error {
	for _, c := range requiredIndexes {
		if err := mongox.EnsureSpecs(ctx, e.db.Collection(c.collection), c.specs); err != nil {
			return fmt.Errorf("notification: %s indexes: %w", c.collection, err)
		}
	}
	return nil
}
