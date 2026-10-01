package challengerclient

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Typed sentinels; callers map them to their own wire errors (infra never imports apperr).
var (
	// ErrNotFound — the challenge id never existed (a deleted one is returned with Deleted = true).
	ErrNotFound = errors.New("challengerclient: challenge not found")
	// ErrInvalidArgument — challenger rejected the request shape; a bug on this side.
	ErrInvalidArgument = errors.New("challengerclient: invalid argument")
	// ErrUnauthenticated — challenger rejected internal-api-secret; a config error (secret drift).
	ErrUnauthenticated = errors.New("challengerclient: internal secret rejected")
	// ErrUnavailable — transient failures outlasted MaxAttempts, or challenger failed internally.
	ErrUnavailable = errors.New("challengerclient: challenger unavailable")
)

// reasonNotFound is challenger's ErrorInfo.reason for an unknown challenge id.
const reasonNotFound = "CHALLENGE_NOT_FOUND"

// translateErr maps a gRPC status to a sentinel, keeping the code (never the message) in the
// text. The caller's own cancel / deadline is returned as the context error.
func translateErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("challengerclient: %w", ctxErr)
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var sentinel error
	switch {
	case st.Code() == codes.NotFound || errorReason(st) == reasonNotFound:
		sentinel = ErrNotFound
	case st.Code() == codes.InvalidArgument:
		sentinel = ErrInvalidArgument
	case st.Code() == codes.Unauthenticated:
		sentinel = ErrUnauthenticated
	default:
		sentinel = ErrUnavailable
	}
	return fmt.Errorf("%w (grpc %s, reason %q)", sentinel, st.Code(), errorReason(st))
}

func errorReason(st *status.Status) string {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}
