// Package grpcclient is the dial helper for outbound gRPC calls. Features
// embed a typed client (auth.Client, etc.) behind their own interface; this
// package only provides the *grpc.ClientConn with sensible defaults +
// functional options.
//
// Why a wrapper rather than calling grpc.NewClient directly:
//   - One place to add interceptors (OTel, retry, timeout, logging) when
//     they're needed across all clients.
//   - One place to switch insecure → TLS when prod lands.
//   - Tests can substitute a Dialer that hits bufconn for handler-level tests.
package grpcclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// We deliberately do NOT expose a dial-timeout option. grpc.NewClient is
// non-blocking — the connection is established lazily on the first RPC, so
// "dial timeout" has no observable behavior to gate. Use a per-call context
// timeout for first-call latency budgeting, and health checks / circuit
// breakers to detect server unavailability.

// options carries dial-time settings. Unexported because callers compose it
// only through the With* functional options below; constructing it directly
// has no use case.
type options struct {
	addr             string
	tlsConfig        *tls.Config
	keepalive        keepalive.ClientParameters
	defaultCallOpts  []grpc.CallOption
	extraDialOptions []grpc.DialOption
}

// Option is the functional-options type.
type Option func(*options)

// WithTLS enables TLS using cfg. If cfg is nil, the system roots are used.
func WithTLS(cfg *tls.Config) Option { return func(o *options) { o.tlsConfig = cfg } }

// WithKeepalive overrides the default keepalive parameters (60s ping, 20s
// timeout, no ping without active streams).
func WithKeepalive(kp keepalive.ClientParameters) Option {
	return func(o *options) { o.keepalive = kp }
}

// WithDialOption appends a raw grpc.DialOption — escape hatch for things that
// don't have a typed Option yet.
func WithDialOption(opts ...grpc.DialOption) Option {
	return func(o *options) { o.extraDialOptions = append(o.extraDialOptions, opts...) }
}

// WithDefaultCallOption sets call options applied to every RPC made through
// the returned connection.
func WithDefaultCallOption(opts ...grpc.CallOption) Option {
	return func(o *options) { o.defaultCallOpts = append(o.defaultCallOpts, opts...) }
}

// Dial opens a gRPC connection to addr. The caller owns the returned conn and
// MUST defer conn.Close() in the bootstrap shutdown path.
//
// Default behavior (no options):
//   - plaintext (insecure) — switch on TLS via WithTLS for prod
//   - keepalive: 60s ping, 20s timeout, no ping without active streams
//
// grpc.NewClient is non-blocking; the connection establishes on first RPC.
// ctx is accepted for forward-compat (and to satisfy the convention that
// constructor-like calls take a context) but has no effect today.
func Dial(_ context.Context, addr string, opts ...Option) (*grpc.ClientConn, error) {
	o := options{
		addr: addr,
		keepalive: keepalive.ClientParameters{
			Time:                60 * time.Second,
			Timeout:             20 * time.Second,
			PermitWithoutStream: false,
		},
	}
	for _, fn := range opts {
		fn(&o)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithKeepaliveParams(o.keepalive),
		// OTel: stats handler emits a client span per RPC + propagates
		// traceparent via gRPC metadata. No-op when no exporter is configured.
		// No business-code changes required.
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}
	if o.tlsConfig != nil {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(o.tlsConfig)))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	if len(o.defaultCallOpts) > 0 {
		dialOpts = append(dialOpts, grpc.WithDefaultCallOptions(o.defaultCallOpts...))
	}
	dialOpts = append(dialOpts, o.extraDialOptions...)

	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		// Defensive: grpc.NewClient is documented to return (nil, err) on
		// failure, but a future driver change could return a half-built conn.
		// Close before returning so we never leak resolver / balancer goroutines.
		if conn != nil {
			// Best-effort cleanup — we're already on the error path and the
			// caller gets a wrapped err; an additional Close error would only
			// add noise. errcheck-suppressed by intent.
			_ = conn.Close() //nolint:errcheck // defensive cleanup on a path we're already failing
		}
		return nil, fmt.Errorf("grpcclient: dial %s: %w", addr, err)
	}
	return conn, nil
}
