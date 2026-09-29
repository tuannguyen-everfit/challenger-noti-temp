package pagination

import (
	"testing"
	"time"
)

func TestCursor_RoundTrip(t *testing.T) {
	c := Cursor{CreatedAt: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC), ID: "abc123"}
	enc := c.Encode()
	if enc == "" {
		t.Fatal("encoded should not be empty")
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.CreatedAt.Equal(c.CreatedAt) || got.ID != c.ID {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, c)
	}
}

func TestCursor_ZeroEncodesEmpty(t *testing.T) {
	zero := Cursor{}
	if zero.Encode() != "" {
		t.Error("zero cursor should encode to empty")
	}
	got, err := Decode("")
	if err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("decode empty got %+v, want zero", got)
	}
}

func TestDecode_GarbageReturnsError(t *testing.T) {
	if _, err := Decode("not-base64!!"); err == nil {
		t.Error("expected error for garbage cursor")
	}
}

func TestQuery_EffectiveLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, DefaultLimit},
		{-1, DefaultLimit},
		{50, 50},
		{200, MaxLimit},
	}
	for _, c := range cases {
		if got := (Query{Limit: c.in}).EffectiveLimit(); got != c.want {
			t.Errorf("EffectiveLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBuild_NoMore(t *testing.T) {
	items := []int{1, 2, 3}
	p := Build(items, 5, func(int) Cursor { return Cursor{ID: "x"} })
	if p.HasMore {
		t.Error("HasMore should be false (under limit)")
	}
	if p.NextCursor != "" {
		t.Errorf("NextCursor should be empty, got %q", p.NextCursor)
	}
	if len(p.Items) != 3 {
		t.Errorf("items = %d, want 3", len(p.Items))
	}
}

func TestBuild_HasMore_TrimsAndEncodes(t *testing.T) {
	// limit+1 elements; last is excess.
	items := []int{1, 2, 3, 4} // limit=3, so 4th triggers HasMore
	p := Build(items, 3, func(v int) Cursor {
		return Cursor{CreatedAt: time.Unix(int64(v), 0).UTC(), ID: "id"}
	})
	if !p.HasMore {
		t.Error("HasMore should be true")
	}
	if len(p.Items) != 3 {
		t.Errorf("items = %d, want 3 (trimmed)", len(p.Items))
	}
	if p.NextCursor == "" {
		t.Error("NextCursor should be set when HasMore")
	}
	// cursor decoded should reflect the LAST kept item (3), not the dropped (4)
	c, _ := Decode(p.NextCursor)
	if c.CreatedAt != time.Unix(3, 0).UTC() {
		t.Errorf("cursor anchor = %v, want unix=3", c.CreatedAt)
	}
}
