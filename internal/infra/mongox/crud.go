package mongox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrNotFound is the canonical "no document" sentinel returned by the
// generic CRUD helpers. Feature repos can wrap this or define their own:
//
//	if errors.Is(err, mongox.ErrNotFound) { return items.ErrNotFound }
var ErrNotFound = errors.New("mongox: not found")

// FindByID looks up a single document by _id. `id` may be any BSON-encodable
// value (string, bson.ObjectID, int, …); the type must match the collection's
// existing _id type. Returns ErrNotFound when no document matches.
//
//	it, err := mongox.FindByID[Item](ctx, r.coll, "my-key")
func FindByID[T any](ctx context.Context, coll *mongo.Collection, id any) (T, error) {
	return FindOne[T](ctx, coll, bson.M{"_id": id})
}

// FindOne is FindByID with a custom filter. Pass field names to project only
// those fields (everything else is excluded; _id is included by default unless
// you list it explicitly with a negative).
//
//	it, err := mongox.FindOne[Item](ctx, r.coll, bson.M{"status": "ACTIVE"}, "title", "owner")
func FindOne[T any](ctx context.Context, coll *mongo.Collection, filter bson.M, fields ...string) (T, error) {
	var out T
	opts := options.FindOne()
	if len(fields) > 0 {
		opts = opts.SetProjection(projection(fields))
	}
	err := coll.FindOne(ctx, filter, opts).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, fmt.Errorf("mongox: find: %w", err)
	}
	return out, nil
}

// FindMany returns all documents matching the filter, with optional projection.
// For large result sets use a cursor-based iteration directly with the driver —
// this helper materializes the full slice.
func FindMany[T any](ctx context.Context, coll *mongo.Collection, filter bson.M, fields ...string) ([]T, error) {
	opts := options.Find()
	if len(fields) > 0 {
		opts = opts.SetProjection(projection(fields))
	}
	cur, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("mongox: find many: %w", err)
	}
	defer func() {
		if cErr := cur.Close(ctx); cErr != nil {
			slog.Default().Warn("mongox: cursor close failed", slog.String("error", cErr.Error()))
		}
	}()
	var out []T
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("mongox: decode many: %w", err)
	}
	return out, nil
}

// UpdateByID applies a `$set` patch to one document by _id. Returns
// ErrNotFound when no document matches. Use ReplaceByID for full-document
// replacement instead of patching individual fields.
//
//	err := mongox.UpdateByID(ctx, r.coll, key, bson.M{"value": "new"})
func UpdateByID(ctx context.Context, coll *mongo.Collection, id any, set bson.M) error {
	res, err := coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	if err != nil {
		return fmt.Errorf("mongox: update: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertByID applies `$set` with upsert=true — creates the document if it
// doesn't exist, patches it otherwise. Use for write-once / write-many flows
// where the caller doesn't care about distinguishing insert vs update.
func UpsertByID(ctx context.Context, coll *mongo.Collection, id any, set bson.M) error {
	opts := options.UpdateOne().SetUpsert(true)
	if _, err := coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set}, opts); err != nil {
		return fmt.Errorf("mongox: upsert: %w", err)
	}
	return nil
}

// DeleteByID removes one document by _id. Returns ErrNotFound when no
// document matches.
func DeleteByID(ctx context.Context, coll *mongo.Collection, id any) error {
	res, err := coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("mongox: delete: %w", err)
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns the number of documents matching the filter.
func Count(ctx context.Context, coll *mongo.Collection, filter bson.M) (int64, error) {
	n, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("mongox: count: %w", err)
	}
	return n, nil
}

// projection builds a bson.M projection from a field list.
//
//	["a", "b"] -> {"a": 1, "b": 1}
func projection(fields []string) bson.M {
	proj := make(bson.M, len(fields))
	for _, f := range fields {
		proj[f] = 1
	}
	return proj
}
