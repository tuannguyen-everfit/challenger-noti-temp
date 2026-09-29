package mongox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// EnsureSpecs validates each spec BEFORE hitting the driver, so the unit
// tests can exercise the validation path without a real Mongo. The
// integration test (index_integration_test.go, build-tag `integration`)
// covers the create-on-real-mongo path.

func TestEnsureSpecs_EmptySliceIsNoOp(t *testing.T) {
	// nil coll is fine — EnsureSpecs short-circuits before dereferencing it.
	if err := EnsureSpecs(context.Background(), nil, nil); err != nil {
		t.Errorf("err = %v, want nil for empty specs", err)
	}
}

func TestEnsureSpecs_MissingNameRejected(t *testing.T) {
	err := EnsureSpecs(context.Background(), nil, []IndexSpec{
		{Keys: bson.D{{Key: "foo", Value: 1}}},
	})
	if err == nil {
		t.Fatalf("err = nil, want validation failure for missing Name")
	}
	if !strings.Contains(err.Error(), "IndexSpec.Name required") {
		t.Errorf("err = %q, want it to mention IndexSpec.Name required", err.Error())
	}
}

func TestEnsureSpecs_MissingKeysRejected(t *testing.T) {
	err := EnsureSpecs(context.Background(), nil, []IndexSpec{
		{Name: "by_foo"},
	})
	if err == nil {
		t.Fatalf("err = nil, want validation failure for empty Keys")
	}
	if !strings.Contains(err.Error(), "IndexSpec.Keys required") {
		t.Errorf("err = %q, want it to mention IndexSpec.Keys required", err.Error())
	}
}

func TestEnsureSpecs_ValidationStopsBeforeDriverCall(t *testing.T) {
	// Two specs: first valid, second missing Keys. Validation must reject the
	// whole batch before any CreateMany attempt — we don't want partial
	// success behavior. (nil coll would crash if CreateMany were called.)
	err := EnsureSpecs(context.Background(), nil, []IndexSpec{
		{Name: "by_foo", Keys: bson.D{{Key: "foo", Value: 1}}},
		{Name: "by_bar"}, // missing Keys
	})
	var target *errFromValidation
	_ = target
	if err == nil {
		t.Fatalf("err = nil, want validation failure")
	}
	if !strings.Contains(err.Error(), "IndexSpec.Keys required") {
		t.Errorf("err = %q, want batch validation to flag the second spec", err.Error())
	}
}

// errFromValidation is a type marker, kept private. Only here so future tests
// can errors.As against a typed validation error if we ever introduce one.
type errFromValidation struct{ msg string }

func (e *errFromValidation) Error() string { return e.msg }

// Compile-time sanity that errors package is wired (in case future fixes use
// errors.Is on the wrapped CreateMany error).
var _ = errors.Is
