package middleware

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain asserts no goroutine outlives the package's tests (concurrency.md §6).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
