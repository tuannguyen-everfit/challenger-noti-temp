// Package pagination implements cursor-based pagination. Cursors are opaque
// base64-encoded JSON of (CreatedAt, ID) — stable under inserts, O(1)
// regardless of position. Use this for every list endpoint; do NOT add an
// offset/page variant.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// DefaultLimit is the page size when the client doesn't specify.
	DefaultLimit = 20
	// MaxLimit caps client-supplied limit values.
	MaxLimit = 100
)

// Cursor encodes the position in a sort key (CreatedAt DESC, then ID DESC for
// tie-break). Choose this tuple as the universal sort key for collections in
// this service; document any deviation per feature.
type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

// IsZero reports whether the cursor has no position (start of list).
func (c Cursor) IsZero() bool { return c.ID == "" && c.CreatedAt.IsZero() }

// Encode returns the opaque string form. Zero cursor encodes to empty string.
func (c Cursor) Encode() string {
	if c.IsZero() {
		return ""
	}
	b, err := json.Marshal(c)
	if err != nil {
		// Cursor is a fixed-shape struct of public types — Marshal cannot fail.
		// Return empty so callers see no NextCursor rather than crashing.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses an opaque cursor string. Empty input returns the zero cursor.
func Decode(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("pagination: decode cursor: %w", err)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, fmt.Errorf("pagination: parse cursor: %w", err)
	}
	return c, nil
}

// Query is the request side of a list endpoint. Bind it from URL params and
// run `validate.Struct(q)` — the limit is clamped via EffectiveLimit.
type Query struct {
	Cursor string `json:"cursor"          validate:"omitempty"`
	Limit  int    `json:"limit,omitempty" validate:"omitempty,min=1,max=100"`
}

// EffectiveLimit returns the limit to actually query — never zero, never above MaxLimit.
func (q Query) EffectiveLimit() int {
	switch {
	case q.Limit <= 0:
		return DefaultLimit
	case q.Limit > MaxLimit:
		return MaxLimit
	default:
		return q.Limit
	}
}

// Page is the response envelope for list endpoints.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// Build assembles a Page from a slice that was queried with limit+1.
// `items` should contain up to limit+1 elements; if it has more than limit,
// the last one is dropped and HasMore=true. nextCursorFn extracts the cursor
// from the LAST kept item.
//
// Example:
//
//	items := repo.Find(filter, sort, q.EffectiveLimit()+1)
//	page := pagination.Build(items, q.EffectiveLimit(), func(it Item) Cursor {
//	    return Cursor{CreatedAt: it.CreatedAt, ID: it.ID}
//	})
func Build[T any](items []T, limit int, nextCursorFn func(T) Cursor) Page[T] {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var next string
	if hasMore && len(items) > 0 {
		next = nextCursorFn(items[len(items)-1]).Encode()
	}
	return Page[T]{Items: items, NextCursor: next, HasMore: hasMore}
}
