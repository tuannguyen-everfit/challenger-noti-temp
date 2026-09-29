// Package kafka wraps the Kafka producer and consumer. Feature code talks to
// the Producer / Consumer interfaces — never imports `segmentio/kafka-go`
// directly. That isolates the driver choice and makes service-layer testing
// trivial (mock the interface).
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Producer publishes JSON-serialized messages to Kafka topics. Feature code
// depends on this interface; the concrete impl is constructed once at boot.
type Producer interface {
	// Publish marshals payload as JSON and writes it to topic with the given
	// key. Key may be empty; when set, Kafka uses it for partition routing.
	Publish(ctx context.Context, topic, key string, payload any) error
	// Close flushes pending writes and releases the underlying connection.
	Close() error
}

// ProducerConfig is the boot-time settings. Brokers is the only required field.
type ProducerConfig struct {
	Brokers      []string
	WriteTimeout time.Duration // default 10s
	BatchTimeout time.Duration // default 50ms
	RequiredAcks kafka.RequiredAcks
}

// NewProducer constructs a Producer that fans out to the configured brokers.
// Use one Producer per process — it's safe for concurrent use.
func NewProducer(cfg ProducerConfig, log *slog.Logger) (Producer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka: at least one broker required")
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	if cfg.BatchTimeout == 0 {
		cfg.BatchTimeout = 50 * time.Millisecond
	}
	if cfg.RequiredAcks == 0 {
		cfg.RequiredAcks = kafka.RequireAll
	}
	w := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Balancer:               &kafka.Hash{},
		WriteTimeout:           cfg.WriteTimeout,
		BatchTimeout:           cfg.BatchTimeout,
		RequiredAcks:           cfg.RequiredAcks,
		AllowAutoTopicCreation: false, // topics created via DevOps, not here
	}
	return &writer{w: w, log: log}, nil
}

type writer struct {
	w   *kafka.Writer
	log *slog.Logger
}

func (p *writer) Publish(ctx context.Context, topic, key string, payload any) error {
	if topic == "" {
		return errors.New("kafka: topic is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("kafka: marshal payload: %w", err)
	}

	// OTel: producer span. The consumer side extracts traceparent from the
	// message headers and starts a child span — so publish and consume show
	// up as one linked trace tree in the collector.
	ctx, span := otel.Tracer(tracerName).Start(ctx, "kafka.publish "+topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String(attrMessagingSystem, systemValueKafka),
			attribute.String(attrMessagingDest, topic),
			attribute.String(attrMessagingOp, opPublish),
			attribute.String(attrMessagingKafkaKey, key),
		),
	)
	defer span.End()

	msg := kafka.Message{
		Topic: topic,
		Key:   []byte(key),
		Value: body,
		Time:  time.Now(),
	}
	// Inject W3C traceparent into the message headers. Use the same propagator
	// otelx.Setup registers (default = tracecontext + baggage).
	hc := headerCarrier(msg.Headers)
	otel.GetTextMapPropagator().Inject(ctx, &hc)
	msg.Headers = []kafka.Header(hc)

	if err := p.w.WriteMessages(ctx, msg); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("kafka: write %s: %w", topic, err)
	}
	p.log.Debug("kafka publish",
		slog.String("topic", topic),
		slog.Int("bytes", len(body)),
	)
	return nil
}

func (p *writer) Close() error { return p.w.Close() }
