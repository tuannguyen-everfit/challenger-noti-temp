package safego

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestWrapErr_RecoversPanic(t *testing.T) {
	wrapped := WrapErr(func() error {
		panic("boom")
	})
	err := wrapped()
	if err == nil {
		t.Fatal("expected non-nil err from recovered panic")
	}
	if !strings.Contains(err.Error(), "panic recovered") {
		t.Errorf("err = %v, want one mentioning panic", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want one containing panic value", err)
	}
}

func TestWrapErr_PassesThroughNormalReturn(t *testing.T) {
	want := errors.New("normal")
	got := WrapErr(func() error { return want })()
	if !errors.Is(got, want) {
		t.Errorf("err = %v, want %v", got, want)
	}
}

func TestWrapErr_NilOnSuccess(t *testing.T) {
	if err := WrapErr(func() error { return nil })(); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestGo_DoesNotCrashOnPanic(t *testing.T) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	Go(func() {
		defer wg.Done()
		panic("boom in Go")
	})
	wg.Wait() // would never return if the panic crashed the test goroutine
}
