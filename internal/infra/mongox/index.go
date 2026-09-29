package mongox

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// IndexSpec is a declarative index definition. Every collection used by a
// feature should declare its required indexes as a `var requiredIndexes
// []IndexSpec` at the top of `repository_mongo.go`. The slice IS the audit
// surface: any query against the collection must map to one of these specs.
//
// Pinned in .claude/rules/mongo.md (Indexes as data) — adding a new query
// pattern without adding (or reusing) a matching IndexSpec is a hard STOP.
type IndexSpec struct {
	// Name is the explicit index name. REQUIRED — never let Mongo auto-derive
	// (auto-generated names collide silently when keys are renamed and leak
	// orphan indexes on the next deploy).
	Name string

	// Keys is the ordered key/direction list. Equality fields first, then
	// sort fields. e.g. bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: -1}}.
	Keys bson.D

	// Unique enforces uniqueness (in addition to the implicit _id index).
	Unique bool

	// Partial restricts the index to documents matching the filter expression
	// (e.g. bson.M{"deleted_at": bson.M{"$exists": false}}). Optional.
	Partial bson.M

	// TTL sets expireAfterSeconds. Use only on single-field date indexes —
	// Mongo's TTL has strict requirements (BSON date field, single key).
	TTL time.Duration

	// Collation sets the index collation. Optional. A query can only use this
	// index for a string predicate when it specifies the SAME collation — set
	// it here AND on the query (e.g. case-insensitive email match on a
	// collection whose stored casing we don't control). Nil = simple/binary.
	Collation *options.Collation
}

// EnsureSpecs creates each index on coll. Mongo is idempotent on
// (name + keys) — re-running is a no-op when the index already exists. A
// (same name, different keys) drift surfaces as an error so an operator
// notices instead of silently keeping a stale index.
//
// Call once per collection at boot via the IndexEnsurer hook; see
// .claude/rules/mongo.md §3 for the per-feature pattern.
func EnsureSpecs(ctx context.Context, coll *mongo.Collection, specs []IndexSpec) error {
	if len(specs) == 0 {
		return nil
	}
	// Validate every spec BEFORE touching coll so unit tests can exercise the
	// validation path without a real Mongo and we never half-create on a
	// partially-invalid batch.
	models := make([]mongo.IndexModel, 0, len(specs))
	for _, s := range specs {
		if s.Name == "" {
			return fmt.Errorf("mongox: IndexSpec.Name required")
		}
		if len(s.Keys) == 0 {
			return fmt.Errorf("mongox: IndexSpec.Keys required for index %q", s.Name)
		}
		opts := options.Index().SetName(s.Name)
		if s.Unique {
			opts = opts.SetUnique(true)
		}
		if s.Partial != nil {
			opts = opts.SetPartialFilterExpression(s.Partial)
		}
		if s.TTL > 0 {
			opts = opts.SetExpireAfterSeconds(int32(s.TTL.Seconds()))
		}
		if s.Collation != nil {
			opts = opts.SetCollation(s.Collation)
		}
		models = append(models, mongo.IndexModel{Keys: s.Keys, Options: opts})
	}
	if _, err := coll.Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("mongox: create indexes on %s: %w", coll.Name(), err)
	}
	return nil
}
