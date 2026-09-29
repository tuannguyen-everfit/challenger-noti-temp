package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	err := Do(context.Background(), func() error { calls++; return nil })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestDo_SucceedsAfterRetries(t *testing.T) {
	calls := 0
	err := Do(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	}, WithInitialBackoff(time.Microsecond), WithoutJitter())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestDo_ExhaustsAndReturnsLastError(t *testing.T) {
	terminal := errors.New("permanent")
	calls := 0
	err := Do(context.Background(), func() error {
		calls++
		return terminal
	}, WithMaxAttempts(3), WithInitialBackoff(time.Microsecond), WithoutJitter())

	if !errors.Is(err, terminal) {
		t.Errorf("err = %v, want %v", err, terminal)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestDo_RetryIfFalse_StopsImmediately(t *testing.T) {
	calls := 0
	err := Do(context.Background(),
		func() error { calls++; return errors.New("4xx") },
		WithMaxAttempts(5),
		WithRetryIf(func(error) bool { return false }),
	)
	if err == nil {
		t.Fatal("err should be non-nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retries)", calls)
	}
}

func TestDo_ContextCanceledMidBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	err := Do(ctx, func() error {
		calls++
		return errors.New("transient")
	}, WithMaxAttempts(10), WithInitialBackoff(100*time.Millisecond), WithoutJitter())

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls < 1 || calls > 2 {
		t.Errorf("calls = %d, want 1 or 2 (interrupted)", calls)
	}
}

func TestDoWithResult_CapturesValueOnSuccess(t *testing.T) {
	got, err := DoWithResult(context.Background(), func() (int, error) {
		return 42, nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestDoWithResult_RetriesUntilSuccess(t *testing.T) {
	calls := 0
	got, err := DoWithResult(context.Background(), func() (string, error) {
		calls++
		if calls < 2 {
			return "", errors.New("flaky")
		}
		return "ok", nil
	}, WithInitialBackoff(time.Microsecond), WithoutJitter())
	if err != nil || got != "ok" || calls != 2 {
		t.Errorf("got=%q calls=%d err=%v", got, calls, err)
	}
}

func TestIsContextError(t *testing.T) {
	if !IsContextError(context.Canceled) {
		t.Error("Canceled should be a context error")
	}
	if !IsContextError(context.DeadlineExceeded) {
		t.Error("DeadlineExceeded should be a context error")
	}
	if IsContextError(errors.New("normal")) {
		t.Error("plain error should not be a context error")
	}
}

func TestBuildOpts_ClampsBadValues(t *testing.T) {
	o := buildOpts([]Option{WithMaxAttempts(0), WithMultiplier(0.5), WithMaxBackoff(time.Nanosecond), WithInitialBackoff(time.Second)})
	if o.MaxAttempts != 1 {
		t.Errorf("MaxAttempts = %d, want clamp to 1", o.MaxAttempts)
	}
	if o.Multiplier < 1 {
		t.Errorf("Multiplier = %f, want clamp to >=1", o.Multiplier)
	}
	if o.MaxBackoff < o.InitialBackoff {
		t.Errorf("MaxBackoff %v < InitialBackoff %v after clamp", o.MaxBackoff, o.InitialBackoff)
	}
}
