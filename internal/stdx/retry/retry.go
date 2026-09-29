// Package retry implements exponential backoff with jitter for outbound calls
// (HTTP, gRPC, Kafka publish, etc.). The wrapper honors ctx cancellation and a
// caller-supplied RetryIf predicate so non-transient errors (4xx, validation)
// don't waste attempts.
//
// Default policy: 3 attempts, 200ms→400ms→800ms backoff with ±25% jitter,
// capped at 10s. Override via functional options.
//
//	err := retry.Do(ctx, func() error {
//	    return client.Publish(ctx, topic, payload)
//	}, retry.WithMaxAttempts(5))
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// Options is the per-call policy. Build it via the With* functional options.
type Options struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
	Jitter         bool
	RetryIf        func(error) bool // return false → stop retrying immediately
}

// Option mutates Options.
type Option func(*Options)

// WithMaxAttempts sets the maximum number of attempts (including the first call).
// Values < 1 are treated as 1 (no retries).
func WithMaxAttempts(n int) Option { return func(o *Options) { o.MaxAttempts = n } }

// WithInitialBackoff sets the first backoff after attempt 1 fails.
func WithInitialBackoff(d time.Duration) Option { return func(o *Options) { o.InitialBackoff = d } }

// WithMaxBackoff caps the exponentially growing backoff.
func WithMaxBackoff(d time.Duration) Option { return func(o *Options) { o.MaxBackoff = d } }

// WithMultiplier sets the backoff growth factor (default 2).
func WithMultiplier(f float64) Option { return func(o *Options) { o.Multiplier = f } }

// WithoutJitter disables the ±25% randomization (use only for deterministic tests).
func WithoutJitter() Option { return func(o *Options) { o.Jitter = false } }

// WithRetryIf sets a predicate that decides whether an error is retriable.
// Default: every error is retriable. Use to skip retries on 4xx-equivalent errors.
func WithRetryIf(pred func(error) bool) Option { return func(o *Options) { o.RetryIf = pred } }

func defaults() Options {
	return Options{
		MaxAttempts:    3,
		InitialBackoff: 200 * time.Millisecond,
		MaxBackoff:     10 * time.Second,
		Multiplier:     2.0,
		Jitter:         true,
		RetryIf:        func(error) bool { return true },
	}
}

// Do runs fn up to MaxAttempts times, sleeping with exponential backoff
// between attempts. Returns nil on success or the LAST error encountered.
// Ctx cancellation is honored both during fn and during the sleep.
func Do(ctx context.Context, fn func() error, opts ...Option) error {
	o := buildOpts(opts)
	var lastErr error
	backoff := o.InitialBackoff

	for attempt := 1; attempt <= o.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !o.RetryIf(lastErr) || attempt == o.MaxAttempts {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(withJitter(backoff, o.Jitter)):
		}
		backoff = nextBackoff(backoff, o.Multiplier, o.MaxBackoff)
	}
	return lastErr
}

// DoWithResult is Do for functions that return a value. The value is captured
// from the successful call (or the zero value on terminal failure).
//
//	result, err := retry.DoWithResult(ctx, func() (Item, error) {
//	    return fetchItem(ctx, id)
//	})
func DoWithResult[T any](ctx context.Context, fn func() (T, error), opts ...Option) (T, error) {
	var captured T
	err := Do(ctx, func() error {
		v, err := fn()
		if err == nil {
			captured = v
		}
		return err
	}, opts...)
	return captured, err
}

func buildOpts(opts []Option) Options {
	o := defaults()
	for _, fn := range opts {
		fn(&o)
	}
	if o.MaxAttempts < 1 {
		o.MaxAttempts = 1
	}
	if o.Multiplier < 1 {
		o.Multiplier = 1
	}
	if o.InitialBackoff < 0 {
		o.InitialBackoff = 0
	}
	if o.MaxBackoff < o.InitialBackoff {
		o.MaxBackoff = o.InitialBackoff
	}
	if o.RetryIf == nil {
		o.RetryIf = func(error) bool { return true }
	}
	return o
}

func nextBackoff(cur time.Duration, mul float64, maxBackoff time.Duration) time.Duration {
	next := time.Duration(float64(cur) * mul)
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

// withJitter randomizes d by ±25% so retried callers don't synchronize.
func withJitter(d time.Duration, on bool) time.Duration {
	if !on || d == 0 {
		return d
	}
	delta := float64(d) * 0.25 * (rand.Float64()*2 - 1) //nolint:gosec // math/rand/v2; not security-sensitive
	return d + time.Duration(delta)
}

// IsContextError reports whether err is a context cancellation/timeout. Useful
// as a RetryIf predicate: don't retry when the caller has given up.
func IsContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// String implements fmt.Stringer for Options — handy for log output at startup.
func (o Options) String() string {
	return fmt.Sprintf("attempts=%d initial=%s max=%s mul=%.1f jitter=%v",
		o.MaxAttempts, o.InitialBackoff, o.MaxBackoff, o.Multiplier, o.Jitter)
}
