// OTel instrumentation for Kafka — producer-side inject + consumer-side
// extract via W3C Trace Context in message headers, so a publish span and
// the consume span on the other side appear as one linked trace tree.
//
// This file lives in the driver wrapper (infra/kafka), not in business code.
// Feature handlers don't see any of this — they receive ctx via the Handler
// signature, and the span is already populated.

package kafka

import (
	"github.com/segmentio/kafka-go"
)

// tracerName matches otelx.tracerName so all spans the service emits share
// one instrumentation scope. Duplicated as a string (not imported) to avoid
// an import cycle between infra/kafka and infra/otelx.
const tracerName = "go-service-template"

// Common attribute keys (OTel messaging semantic conventions). Inlined as
// strings to avoid pinning to a specific semconv version.
const (
	attrMessagingSystem   = "messaging.system"
	attrMessagingDest     = "messaging.destination.name"
	attrMessagingOp       = "messaging.operation"
	attrMessagingKafkaKey = "messaging.kafka.message.key"
	systemValueKafka      = "kafka"
	opPublish             = "publish"
	opProcess             = "process"
)

// headerCarrier adapts []kafka.Header to propagation.TextMapCarrier so the
// OTel propagator can read/write `traceparent` (W3C) on Kafka messages.
//
// kafka-go uses []kafka.Header (slice of struct), not a map — so Set has to
// scan + replace or append. Cost is negligible (handful of headers per msg).
type headerCarrier []kafka.Header

func (c headerCarrier) Get(key string) string {
	for _, h := range c {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c *headerCarrier) Set(key, value string) {
	for i := range *c {
		if (*c)[i].Key == key {
			(*c)[i].Value = []byte(value)
			return
		}
	}
	*c = append(*c, kafka.Header{Key: key, Value: []byte(value)})
}

func (c headerCarrier) Keys() []string {
	out := make([]string, len(c))
	for i, h := range c {
		out[i] = h.Key
	}
	return out
}
