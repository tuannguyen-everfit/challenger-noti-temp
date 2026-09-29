package ptr

import (
	"testing"
	"time"
)

func TestOf_TypeInferred(t *testing.T) {
	s := Of("hello")
	if *s != "hello" {
		t.Errorf("Of[string] *s = %q", *s)
	}
	n := Of(42)
	if *n != 42 {
		t.Errorf("Of[int] *n = %d", *n)
	}
	tm := Of(time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC))
	if tm.Year() != 2026 {
		t.Errorf("Of[time.Time] year = %d", tm.Year())
	}
}

func TestDeref_NilReturnsZero(t *testing.T) {
	var p *string
	if got := Deref(p); got != "" {
		t.Errorf("Deref nil = %q, want \"\"", got)
	}
}

func TestDeref_NonNilReturnsValue(t *testing.T) {
	s := "hello"
	if got := Deref(&s); got != "hello" {
		t.Errorf("Deref = %q", got)
	}
}

func TestDerefOr(t *testing.T) {
	var p *int
	if got := DerefOr(p, 42); got != 42 {
		t.Errorf("nil → fallback: got %d, want 42", got)
	}
	x := 7
	if got := DerefOr(&x, 42); got != 7 {
		t.Errorf("non-nil → value: got %d, want 7", got)
	}
}

func TestOfNotZero_StringZero(t *testing.T) {
	if Of := OfNotZero(""); Of != nil {
		t.Errorf("zero string → ptr (%v), want nil", *Of)
	}
	if p := OfNotZero("x"); p == nil || *p != "x" {
		t.Errorf("non-zero string → wrong ptr")
	}
}

func TestOfNotZero_IntZero(t *testing.T) {
	if p := OfNotZero(0); p != nil {
		t.Error("zero int should produce nil")
	}
	if p := OfNotZero(5); p == nil || *p != 5 {
		t.Error("non-zero int should produce pointer")
	}
}
