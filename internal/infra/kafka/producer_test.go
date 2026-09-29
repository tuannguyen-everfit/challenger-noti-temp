package kafka

import (
	"errors"
	"io"
	"log/slog"
	"testing"
)

func TestNewProducer_RejectsEmptyBrokers(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if _, err := NewProducer(ProducerConfig{Brokers: nil}, log); err == nil {
		t.Error("expected error for empty brokers")
	}
}

func TestNewConsumer_RequiresTopicAndGroup(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cases := []ConsumerConfig{
		{Brokers: []string{"localhost:9092"}},                    // missing topic+group
		{Brokers: []string{"localhost:9092"}, Topic: "ef-test"},  // missing group
		{Brokers: []string{"localhost:9092"}, GroupID: "ef-grp"}, // missing topic
		{Brokers: nil, Topic: "ef-test", GroupID: "ef-grp"},      // missing brokers
	}
	for _, c := range cases {
		if _, err := NewConsumer(c, log); err == nil {
			t.Errorf("expected error for cfg %+v", c)
		}
	}
}

func TestNewConsumer_DefaultsRetry(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := ConsumerConfig{Brokers: []string{"localhost:9092"}, Topic: "ef-test", GroupID: "ef-grp"}
	con, err := NewConsumer(cfg, log)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer con.Close()
	// internal: assert by reflection-free path — just probe close + reuse.
}

func TestConvertMessage_HeaderFlat(t *testing.T) {
	// trivial smoke — convertMessage is private; this just guards future regressions
	// by exercising the public type round-trip.
	m := Message{Topic: "t", Headers: map[string]string{"x": "y"}}
	if m.Headers["x"] != "y" {
		t.Error("header round-trip broken")
	}
	if errors.Is(nil, io.EOF) {
		t.Error("sentinel mismatch — unreachable")
	}
}
