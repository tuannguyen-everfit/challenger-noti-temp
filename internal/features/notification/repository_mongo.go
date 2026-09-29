package notification

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Everfit-io/go-service-template/internal/infra/mongox"
)

type mongoRepo struct{ coll *mongo.Collection }

// NewMongoRepository returns the MongoDB-backed Repo.
func NewMongoRepository(m *mongox.Client) Repo {
	return &mongoRepo{coll: m.DB.Collection(collectionName)}
}

// ListFeed uses index `notifications_feed` (TabAll) or `notifications_feed_tab`.
func (r *mongoRepo) ListFeed(ctx context.Context, userID bson.ObjectID, tab Tab, after feedCursor, limit int) ([]Notification, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "activity_at", Value: -1}, {Key: "_id", Value: -1}}).
		SetLimit(int64(limit))
	cur, err := r.coll.Find(ctx, buildFeedFilter(userID, tab, after), opts)
	if err != nil {
		return nil, fmt.Errorf("notification: list feed: %w", err)
	}
	var out []Notification
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("notification: decode feed: %w", err)
	}
	return out, nil
}

// CountUnread uses index `notifications_unread`.
func (r *mongoRepo) CountUnread(ctx context.Context, userID bson.ObjectID, tab Tab) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, buildUnreadFilter(userID, tab), options.Count().SetLimit(summaryCountCap))
	if err != nil {
		return 0, fmt.Errorf("notification: count unread: %w", err)
	}
	return n, nil
}

// CountRange uses index `notifications_feed` (TabAll) or `notifications_feed_tab`.
func (r *mongoRepo) CountRange(ctx context.Context, userID bson.ObjectID, tab Tab, from, to time.Time) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, buildRangeFilter(userID, tab, from, to), options.Count().SetLimit(summaryCountCap))
	if err != nil {
		return 0, fmt.Errorf("notification: count range: %w", err)
	}
	return n, nil
}

func buildFeedFilter(userID bson.ObjectID, tab Tab, after feedCursor) bson.M {
	filter := buildOwnerFilter(userID, tab)
	if !after.IsZero() {
		// Top-level $lte keeps the index bounds tight whatever the planner does with the $or.
		filter["activity_at"] = bson.M{"$lte": after.ActivityAt}
		filter["$or"] = bson.A{
			bson.M{"activity_at": bson.M{"$lt": after.ActivityAt}},
			bson.M{"activity_at": after.ActivityAt, "_id": bson.M{"$lt": after.ID}},
		}
	}
	return filter
}

// buildUnreadFilter matches `read_at` null or missing; writers un-read with $unset, never $set: null.
func buildUnreadFilter(userID bson.ObjectID, tab Tab) bson.M {
	return bson.M{"user_id": userID, "tab": tab, "read_at": nil}
}

func buildRangeFilter(userID bson.ObjectID, tab Tab, from, to time.Time) bson.M {
	filter := buildOwnerFilter(userID, tab)
	bounds := bson.M{}
	if !from.IsZero() {
		bounds["$gte"] = from
	}
	if !to.IsZero() {
		bounds["$lt"] = to
	}
	if len(bounds) > 0 {
		filter["activity_at"] = bounds
	}
	return filter
}

func buildOwnerFilter(userID bson.ObjectID, tab Tab) bson.M {
	filter := bson.M{"user_id": userID}
	if tab != TabAll {
		filter["tab"] = tab
	}
	return filter
}
