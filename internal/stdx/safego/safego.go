// Package safego wraps goroutine launches so panics become returned errors
// (or logged) instead of crashing the process.
//
// Use this for ANY goroutine launched from request scope — a single panic in
// a worker goroutine takes the whole binary down by default. Two flavors:
//
//	g, ctx := errgroup.WithContext(ctx)
//	g.Go(safego.WrapErr(func() error { return doRiskyWork(ctx) }))
//
//	// fire-and-forget background task
//	safego.Go(func() { doRiskyWork(ctx) })
package safego

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// WrapErr wraps fn so a panic becomes a returned error containing the panic
// value + stack. Designed for use with errgroup, where one goroutine's panic
// would otherwise crash the process while the others run concurrently.
func WrapErr(fn func() error) func() error {
	return func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic recovered: %v\n%s", r, debug.Stack())
			}
		}()
		return fn()
	}
}

// Go runs fn in a new goroutine, recovering any panic and logging it via the
// default slog logger. Use for fire-and-forget background work where there's
// no caller to receive the error.
func Go(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Default().Error("safego: panic recovered",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
			}
		}()
		fn()
	}()
}
