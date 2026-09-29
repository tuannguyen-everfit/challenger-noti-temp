package kafka

import (
	"context"
	"encoding/json"
	"log/slog"
)

// JSONHandler wraps a typed event handler into the message-level Handler
// signature expected by Consumer.Run.
//
// The wrapper applies a fixed policy:
//
//   - decode failure → LOG the error and return nil. The offset is committed
//     so the consumer moves on. Retrying a malformed payload would never
//     succeed; surfacing it as a handler error would just churn the DLQ.
//   - handler error  → propagate. Consumer.Run's retry + DLQ policy applies.
//   - handler nil    → propagate nil. Offset commits.
//
// Use:
//
//	c.Run(ctx, kafka.JSONHandler[items.PublishedEvent](log, svc.OnItemPublished))
//
// `fn` MUST be idempotent — Kafka is at-least-once. Dedupe on a payload field
// (event_id, version, …) inside the handler.
func JSONHandler[T any](log *slog.Logger, fn func(ctx context.Context, evt T) error) Handler {
	return func(ctx context.Context, msg Message) error {
		var evt T
		if err := json.Unmarshal(msg.Value, &evt); err != nil {
			log.Error("malformed event — committing and skipping",
				slog.Int64("offset", msg.Offset),
				slog.String("error", err.Error()),
			)
			return nil
		}
		return fn(ctx, evt)
	}
}
