package grpcclient

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

func TestDial_FreshConnectionIsIdle(t *testing.T) {
	conn, err := Dial(context.Background(), "127.0.0.1:1") // nothing listening
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	// State is IDLE on a fresh non-blocking dial — connection happens on first RPC.
	if got := conn.GetState().String(); got != "IDLE" {
		t.Errorf("state = %q, want IDLE on fresh dial", got)
	}
}

func TestDial_AppliesOptions(t *testing.T) {
	// Just exercises the option-application path — no actual connection check.
	conn, err := Dial(context.Background(), "127.0.0.1:1",
		WithTLS(&tls.Config{MinVersion: tls.VersionTLS13}),
	)
	if err != nil {
		t.Fatalf("Dial with options: %v", err)
	}
	defer conn.Close()
}

func TestDial_AllOptionBuilders(t *testing.T) {
	// Exercises every option builder so coverage for WithKeepalive,
	// WithDialOption, WithDefaultCallOption gets exercised through Dial.
	conn, err := Dial(context.Background(), "127.0.0.1:1",
		WithKeepalive(keepalive.ClientParameters{
			Time:                90 * time.Second,
			Timeout:             30 * time.Second,
			PermitWithoutStream: true,
		}),
		WithDialOption(grpc.WithUserAgent("go-service-template-test/0.0")),
		WithDefaultCallOption(grpc.MaxCallRecvMsgSize(8*1024*1024)),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
}

func TestOption_BuildersMutateOptionsStruct(t *testing.T) {
	// Direct unit-level sanity: each builder writes the expected field on the
	// internal options. Belt-and-suspenders against silent breakage if Dial's
	// composition changes.
	var o options
	WithTLS(&tls.Config{MinVersion: tls.VersionTLS12})(&o)
	if o.tlsConfig == nil {
		t.Error("WithTLS did not set tlsConfig")
	}
	WithKeepalive(keepalive.ClientParameters{Time: 7 * time.Second})(&o)
	if o.keepalive.Time != 7*time.Second {
		t.Errorf("WithKeepalive Time = %v, want 7s", o.keepalive.Time)
	}
	WithDialOption(grpc.WithUserAgent("a"), grpc.WithUserAgent("b"))(&o)
	if len(o.extraDialOptions) != 2 {
		t.Errorf("extraDialOptions len = %d, want 2", len(o.extraDialOptions))
	}
	WithDefaultCallOption(grpc.MaxCallRecvMsgSize(1024))(&o)
	if len(o.defaultCallOpts) != 1 {
		t.Errorf("defaultCallOpts len = %d, want 1", len(o.defaultCallOpts))
	}
}
