package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

type testEvent struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestJSONHandler_ForwardsDecodedEvent(t *testing.T) {
	var got testEvent
	h := JSONHandler[testEvent](discardLog(), func(_ context.Context, e testEvent) error {
		got = e
		return nil
	})

	payload, _ := json.Marshal(testEvent{ID: "abc", Count: 42})
	if err := h(context.Background(), Message{Value: payload, Offset: 100}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.ID != "abc" || got.Count != 42 {
		t.Errorf("got = %+v, want {abc 42}", got)
	}
}

func TestJSONHandler_MalformedPayload_CommitsAndLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	called := false
	h := JSONHandler[testEvent](log, func(_ context.Context, _ testEvent) error {
		called = true
		return nil
	})

	err := h(context.Background(), Message{Value: []byte("not json{"), Offset: 7})
	if err != nil {
		t.Errorf("err = %v, want nil (decode failure must commit, not retry)", err)
	}
	if called {
		t.Error("handler must not be invoked on decode failure")
	}
	if !strings.Contains(buf.String(), "offset=7") || !strings.Contains(buf.String(), "malformed") {
		t.Errorf("expected malformed-event log with offset, got: %s", buf.String())
	}
}

func TestJSONHandler_HandlerErrorPropagates(t *testing.T) {
	boom := errors.New("downstream service unavailable")
	h := JSONHandler[testEvent](discardLog(), func(_ context.Context, _ testEvent) error {
		return boom
	})

	payload, _ := json.Marshal(testEvent{ID: "x"})
	err := h(context.Background(), Message{Value: payload})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want chain to %v (so Consumer.Run retries + DLQs)", err, boom)
	}
}
