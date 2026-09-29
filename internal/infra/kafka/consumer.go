package kafka

import (
	"context"
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

// Handler processes a single message. Return:
//   - nil           → commit offset, move on.
//   - non-nil error → log + retry per the consumer's backoff. After N retries
//     the message goes to the DLQ (left to the operator —
//     manual investigation, no auto-purge).
//
// Handlers MUST be idempotent — Kafka semantics are at-least-once delivery.
// Dedupe on a feature-specific idempotency key (e.g. event_id in payload).
type Handler func(ctx context.Context, msg Message) error

// Message is the driver-agnostic shape passed to handlers. Headers exposed as
// a flat map — convert from kafka-go's []kafka.Header internally.
type Message struct {
	Topic     string
	Partition int
	Offset    int64
	Key       []byte
	Value     []byte
	Time      time.Time
	Headers   map[string]string
}

// ConsumerConfig describes one consumer group binding to one topic.
type ConsumerConfig struct {
	Brokers []string
	Topic   string
	GroupID string // consumer group; matches `ch-<subscriber>-<event-name>` convention

	// MaxRetries is the per-message retry count before giving up (default 3).
	MaxRetries int
	// RetryBackoff is the initial backoff (default 500ms, doubled per attempt).
	RetryBackoff time.Duration
}

// Consumer is the consumer-side interface. Use Run(ctx, handler) and cancel
// ctx to stop. Run blocks until ctx is cancelled.
type Consumer interface {
	Run(ctx context.Context, h Handler) error
	Close() error
}

// NewConsumer creates a kafka-go reader bound to topic + group.
func NewConsumer(cfg ConsumerConfig, log *slog.Logger) (Consumer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka: at least one broker required")
	}
	if cfg.Topic == "" || cfg.GroupID == "" {
		return nil, errors.New("kafka: topic and group_id required")
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryBackoff == 0 {
		cfg.RetryBackoff = 500 * time.Millisecond
	}
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.Brokers,
		Topic:          cfg.Topic,
		GroupID:        cfg.GroupID,
		MinBytes:       1,
		MaxBytes:       10 << 20, // 10 MiB
		CommitInterval: 0,        // manual commit — only after handler succeeds
	})
	return &reader{r: r, cfg: cfg, log: log.With(
		slog.String("kafka.topic", cfg.Topic),
		slog.String("kafka.group", cfg.GroupID),
	)}, nil
}

type reader struct {
	r   *kafka.Reader
	cfg ConsumerConfig
	log *slog.Logger
}

func (c *reader) Run(ctx context.Context, h Handler) error {
	c.log.Info("kafka consumer started")
	for {
		m, err := c.r.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("kafka: fetch: %w", err)
		}
		c.processWithRetry(ctx, m, h)
	}
}

func (c *reader) processWithRetry(ctx context.Context, m kafka.Message, h Handler) {
	// OTel: extract producer's traceparent from message headers and start a
	// child span. Retries happen INSIDE this single process span — attempts
	// are not separate spans (they'd be noise in the trace tree); attempt
	// count is recorded as an attribute when we end the span.
	hc := headerCarrier(m.Headers)
	ctx = otel.GetTextMapPropagator().Extract(ctx, &hc)
	ctx, span := otel.Tracer(tracerName).Start(ctx, "kafka.consume "+m.Topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String(attrMessagingSystem, systemValueKafka),
			attribute.String(attrMessagingDest, m.Topic),
			attribute.String(attrMessagingOp, opProcess),
			attribute.String(attrMessagingKafkaKey, string(m.Key)),
			attribute.Int64("messaging.kafka.message.offset", m.Offset),
		),
	)
	defer span.End()

	msg := convertMessage(m)
	backoff := c.cfg.RetryBackoff
	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxRetries; attempt++ {
		err := h(ctx, msg)
		if err == nil {
			if attempt > 1 {
				span.SetAttributes(attribute.Int("messaging.kafka.attempts", attempt))
			}
			if cErr := c.r.CommitMessages(ctx, m); cErr != nil {
				c.log.Warn("commit failed", slog.String("error", cErr.Error()))
			}
			return
		}
		lastErr = err
		c.log.Warn("handler failed",
			slog.Int("attempt", attempt),
			slog.Int64("offset", m.Offset),
			slog.String("error", err.Error()),
		)
		if attempt == c.cfg.MaxRetries {
			c.log.Error("handler exhausted retries — message left uncommitted",
				slog.Int64("offset", m.Offset),
			)
			span.RecordError(lastErr)
			span.SetStatus(codes.Error, lastErr.Error())
			span.SetAttributes(attribute.Int("messaging.kafka.attempts", attempt))
			return // DO NOT commit — operator inspects + reprocesses
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

func (c *reader) Close() error { return c.r.Close() }

func convertMessage(m kafka.Message) Message {
	hdrs := make(map[string]string, len(m.Headers))
	for _, h := range m.Headers {
		hdrs[h.Key] = string(h.Value)
	}
	return Message{
		Topic:     m.Topic,
		Partition: m.Partition,
		Offset:    m.Offset,
		Key:       m.Key,
		Value:     m.Value,
		Time:      m.Time,
		Headers:   hdrs,
	}
}
