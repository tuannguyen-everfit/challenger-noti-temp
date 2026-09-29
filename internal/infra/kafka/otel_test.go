package kafka

import (
	"testing"

	"github.com/segmentio/kafka-go"
)

func TestHeaderCarrier_GetSetKeys(t *testing.T) {
	var c headerCarrier
	if got := c.Get("missing"); got != "" {
		t.Errorf("Get on empty carrier = %q, want empty", got)
	}

	c.Set("traceparent", "00-abc-def-01")
	c.Set("baggage", "k=v")

	if got := c.Get("traceparent"); got != "00-abc-def-01" {
		t.Errorf("Get traceparent = %q", got)
	}
	if got := c.Get("baggage"); got != "k=v" {
		t.Errorf("Get baggage = %q", got)
	}

	// Set on existing key replaces, not appends.
	c.Set("traceparent", "00-xyz-uvw-01")
	if got := c.Get("traceparent"); got != "00-xyz-uvw-01" {
		t.Errorf("replace failed: %q", got)
	}
	if len(c) != 2 {
		t.Errorf("len = %d, want 2 after replace", len(c))
	}

	keys := c.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys() len = %d, want 2", len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	if !seen["traceparent"] || !seen["baggage"] {
		t.Errorf("Keys() missing entries: %v", keys)
	}
}

func TestHeaderCarrier_RoundTrip_WithKafkaHeaderSlice(t *testing.T) {
	// Simulate the producer→consumer round trip at the carrier level:
	// 1. Producer Sets traceparent on an empty carrier.
	// 2. The carrier converts to []kafka.Header (what kafka-go stores).
	// 3. Consumer wraps that slice as headerCarrier and Gets the value.
	var prod headerCarrier
	prod.Set("traceparent", "00-deadbeef-feedface-01")

	wire := []kafka.Header(prod)
	cons := headerCarrier(wire)
	if got := cons.Get("traceparent"); got != "00-deadbeef-feedface-01" {
		t.Errorf("round-trip lost traceparent: got %q", got)
	}
}
