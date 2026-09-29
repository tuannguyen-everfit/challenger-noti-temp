package notification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Everfit-io/go-service-template/internal/infra/mongox"
)

type mongoRepo struct {
	client  *mongox.Client
	coll    *mongo.Collection
	devices *mongo.Collection
}

// NewMongoRepository returns the MongoDB-backed Repo over notifications and notification_devices.
func NewMongoRepository(m *mongox.Client) Repo {
	return &mongoRepo{client: m, coll: m.DB.Collection(collectionName), devices: m.DB.Collection(deviceCollectionName)}
}

type mongoAuditWriter struct{ coll *mongo.Collection }

// NewMongoAuditWriter returns the MongoDB-backed AuditWriter over notification_audit_logs.
func NewMongoAuditWriter(m *mongox.Client) AuditWriter {
	return &mongoAuditWriter{coll: m.DB.Collection(auditCollectionName)}
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

// WithTransaction delegates to mongox; both adapters join the transaction through fn's ctx.
func (r *mongoRepo) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.client.WithTransaction(ctx, fn)
}

// FindDevice uses index `devices_user_device`.
func (r *mongoRepo) FindDevice(ctx context.Context, userID bson.ObjectID, deviceID string) (Device, bool, error) {
	return r.findDevice(ctx, buildDeviceKeyFilter(userID, deviceID))
}

// FindDeviceByToken uses index `devices_token`.
func (r *mongoRepo) FindDeviceByToken(ctx context.Context, token string) (Device, bool, error) {
	return r.findDevice(ctx, bson.M{"token": token})
}

// UpsertDevice uses index `devices_user_device`.
func (r *mongoRepo) UpsertDevice(ctx context.Context, d Device) (bson.ObjectID, error) {
	res, err := r.devices.UpdateOne(ctx, buildDeviceKeyFilter(d.UserID, d.DeviceID), buildDeviceUpsert(d), options.UpdateOne().SetUpsert(true))
	if err != nil {
		return bson.ObjectID{}, fmt.Errorf("notification: upsert device: %w", err)
	}
	if id, ok := res.UpsertedID.(bson.ObjectID); ok {
		return id, nil
	}
	return d.ID, nil
}

// TouchDevice uses the `_id` index.
func (r *mongoRepo) TouchDevice(ctx context.Context, id bson.ObjectID, at time.Time) error {
	if _, err := r.devices.UpdateByID(ctx, id, bson.M{"$set": bson.M{"last_registered_at": at}}); err != nil {
		return fmt.Errorf("notification: touch device: %w", err)
	}
	return nil
}

// DeleteDevice uses the `_id` index.
func (r *mongoRepo) DeleteDevice(ctx context.Context, id bson.ObjectID) (bool, error) {
	res, err := r.devices.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return false, fmt.Errorf("notification: delete device: %w", err)
	}
	return res.DeletedCount > 0, nil
}

// FindNotification uses the `_id` index; user_id makes another user's row look absent.
func (r *mongoRepo) FindNotification(ctx context.Context, userID, id bson.ObjectID) (Notification, bool, error) {
	var n Notification
	err := r.coll.FindOne(ctx, bson.M{"_id": id, "user_id": userID}).Decode(&n)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Notification{}, false, nil
	}
	if err != nil {
		return Notification{}, false, fmt.Errorf("notification: find: %w", err)
	}
	return n, true, nil
}

// UpdateRead uses the `_id` index.
func (r *mongoRepo) UpdateRead(ctx context.Context, id bson.ObjectID, u readUpdate) error {
	if _, err := r.coll.UpdateByID(ctx, id, bson.M{"$set": buildReadSet(u)}); err != nil {
		return fmt.Errorf("notification: update read: %w", err)
	}
	return nil
}

// MarkAllRead uses index `notifications_unread` (both tabs listed so the bounds stay tight).
func (r *mongoRepo) MarkAllRead(ctx context.Context, userID bson.ObjectID, at time.Time, by Actor) (int64, error) {
	res, err := r.coll.UpdateMany(ctx, buildMarkAllReadFilter(userID), bson.M{"$set": bson.M{
		"read_at":     at,
		"read_action": ReadActionReadAll,
		"updated_at":  at,
		"updated_by":  by,
	}})
	if err != nil {
		return 0, fmt.Errorf("notification: mark all read: %w", err)
	}
	return res.ModifiedCount, nil
}

// DeleteNotificationsByUser uses index `notifications_feed` (user_id prefix).
func (r *mongoRepo) DeleteNotificationsByUser(ctx context.Context, userID bson.ObjectID) (int64, error) {
	res, err := r.coll.DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, fmt.Errorf("notification: delete user notifications: %w", err)
	}
	return res.DeletedCount, nil
}

// DeleteDevicesByUser uses index `devices_user_device` (user_id prefix).
func (r *mongoRepo) DeleteDevicesByUser(ctx context.Context, userID bson.ObjectID) (int64, error) {
	res, err := r.devices.DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, fmt.Errorf("notification: delete user devices: %w", err)
	}
	return res.DeletedCount, nil
}

func (r *mongoRepo) findDevice(ctx context.Context, filter bson.M) (Device, bool, error) {
	var d Device
	err := r.devices.FindOne(ctx, filter).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, fmt.Errorf("notification: find device: %w", err)
	}
	return d, true, nil
}

// Write inserts e; audit_key is unique (index `audit_dedupe`), so a duplicate is a retried write.
func (w *mongoAuditWriter) Write(ctx context.Context, e AuditLog) error {
	_, err := w.coll.InsertOne(ctx, e)
	if err == nil {
		return nil
	}
	if e.AuditKey != "" && mongo.IsDuplicateKeyError(err) {
		return errAuditRecorded
	}
	return fmt.Errorf("notification: insert audit: %w", err)
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

func buildDeviceKeyFilter(userID bson.ObjectID, deviceID string) bson.M {
	return bson.M{"user_id": userID, "device_id": deviceID}
}

// buildDeviceUpsert sets the registration fields; created_* are insert-only (mongo.md §6) and an
// empty app_version is removed rather than stored as "".
func buildDeviceUpsert(d Device) bson.M {
	set := bson.M{
		"platform":           d.Platform,
		"token":              d.Token,
		"last_registered_at": d.LastRegisteredAt,
		"updated_at":         d.UpdatedAt,
		"updated_by":         d.UpdatedBy,
	}
	update := bson.M{
		"$set":         set,
		"$setOnInsert": bson.M{"created_at": d.CreatedAt, "created_by": d.CreatedBy},
	}
	if d.AppVersion == "" {
		update["$unset"] = bson.M{"app_version": ""}
	} else {
		set["app_version"] = d.AppVersion
	}
	return update
}

// buildMarkAllReadFilter matches the user's unread rows ($unset / missing read_at) in both tabs.
func buildMarkAllReadFilter(userID bson.ObjectID) bson.M {
	return bson.M{"user_id": userID, "tab": bson.M{"$in": bson.A{TabActivities, TabSystem}}, "read_at": nil}
}

func buildReadSet(u readUpdate) bson.M {
	set := bson.M{"updated_at": u.UpdatedAt, "updated_by": u.UpdatedBy}
	if u.ReadAt != nil {
		set["read_at"] = *u.ReadAt
		set["read_action"] = u.ReadAction
	}
	if u.ButtonsHiddenAt != nil {
		set["buttons_hidden_at"] = *u.ButtonsHiddenAt
	}
	return set
}
