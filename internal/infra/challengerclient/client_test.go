package challengerclient

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Everfit-io/go-service-template/internal/stdx/safego"
	"github.com/Everfit-io/go-service-template/proto/challenger/internalv1"
)

// mockChallengeServer answers per attempt and records what each call carried.
type mockChallengeServer struct {
	internalv1.UnimplementedChallengeInternalServiceServer

	mu        sync.Mutex
	getCalls  int
	accCalls  int
	secrets   []string
	sources   []string
	deadlines []bool
	getResp   func(attempt int, req *internalv1.GetChallengeRequest) (*internalv1.GetChallengeResponse, error)
	accResp   func(attempt int, req *internalv1.CheckChallengeAccessRequest) (*internalv1.CheckChallengeAccessResponse, error)
}

func (m *mockChallengeServer) record(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	first := func(key string) string {
		if v := md.Get(key); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	_, hasDeadline := ctx.Deadline()
	m.secrets = append(m.secrets, first(metadataSecret))
	m.sources = append(m.sources, first(metadataSource))
	m.deadlines = append(m.deadlines, hasDeadline)
}

func (m *mockChallengeServer) GetChallenge(ctx context.Context, req *internalv1.GetChallengeRequest) (*internalv1.GetChallengeResponse, error) {
	m.mu.Lock()
	m.getCalls++
	attempt := m.getCalls
	m.record(ctx)
	m.mu.Unlock()
	return m.getResp(attempt, req)
}

func (m *mockChallengeServer) CheckChallengeAccess(ctx context.Context, req *internalv1.CheckChallengeAccessRequest) (*internalv1.CheckChallengeAccessResponse, error) {
	m.mu.Lock()
	m.accCalls++
	attempt := m.accCalls
	m.record(ctx)
	m.mu.Unlock()
	return m.accResp(attempt, req)
}

func (m *mockChallengeServer) calls() (get, access int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls, m.accCalls
}

func newTestClient(t *testing.T, srv *mockChallengeServer) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	internalv1.RegisterChallengeInternalServiceServer(gs, srv)
	done := make(chan struct{})
	safego.Go(func() {
		defer close(done)
		_ = gs.Serve(lis) //nolint:errcheck // Serve returns once Stop runs in Cleanup
	})
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close() //nolint:errcheck // listener already closed by Stop on most paths
		<-done
	})

	c, err := New(context.Background(), Config{
		Addr:         "passthrough:///bufconn",
		Secret:       "test-secret",
		Source:       "challenger-notification-test",
		CallTimeout:  time.Second,
		MaxAttempts:  3,
		RetryBackoff: time.Nanosecond,
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c
}

func statusWithReason(code codes.Code, reason string) error {
	st, err := status.New(code, "fixed server message").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "challenger.internal"})
	if err != nil {
		panic(err)
	}
	return st.Err()
}

var (
	testStart = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	testEnd   = time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
)

func challengeProto(id string) *internalv1.Challenge {
	return &internalv1.Challenge{
		Id: id, Name: "Run Club", Status: internalv1.ChallengeStatus_CHALLENGE_STATUS_PRIVATE,
		StartsAt: timestamppb.New(testStart), EndsAt: timestamppb.New(testEnd),
		ThumbnailUrl: "https://cdn/x.png", ShortDescription: "short", Tagline: "tag", Deleted: true,
	}
}

func TestGetChallenge_MapsAndSendsMetadata(t *testing.T) {
	srv := &mockChallengeServer{getResp: func(_ int, req *internalv1.GetChallengeRequest) (*internalv1.GetChallengeResponse, error) {
		return &internalv1.GetChallengeResponse{Challenge: challengeProto(req.GetChallengeId())}, nil
	}}
	c := newTestClient(t, srv)

	got, err := c.GetChallenge(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	want := Challenge{
		ID: "c1", Name: "Run Club", Status: ChallengeStatusPrivate, StartsAt: testStart, EndsAt: testEnd,
		ThumbnailURL: "https://cdn/x.png", ShortDescription: "short", Tagline: "tag", Deleted: true,
	}
	if got != want {
		t.Errorf("challenge = %+v, want %+v", got, want)
	}
	if srv.secrets[0] != "test-secret" || srv.sources[0] != "challenger-notification-test" || !srv.deadlines[0] {
		t.Errorf("metadata secret=%q source=%q deadline=%v", srv.secrets[0], srv.sources[0], srv.deadlines[0])
	}
}

func TestGetChallenge_UnsetTimesAndUnknownStatus(t *testing.T) {
	srv := &mockChallengeServer{getResp: func(int, *internalv1.GetChallengeRequest) (*internalv1.GetChallengeResponse, error) {
		return &internalv1.GetChallengeResponse{Challenge: &internalv1.Challenge{Id: "c1", Status: internalv1.ChallengeStatus(99)}}, nil
	}}
	got, err := newTestClient(t, srv).GetChallenge(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if got.Status != ChallengeStatusUnknown || !got.StartsAt.IsZero() || !got.EndsAt.IsZero() {
		t.Errorf("challenge = %+v, want unknown status and zero times", got)
	}
}

func TestGetChallenge_CachedForTTL(t *testing.T) {
	srv := &mockChallengeServer{getResp: func(_ int, req *internalv1.GetChallengeRequest) (*internalv1.GetChallengeResponse, error) {
		return &internalv1.GetChallengeResponse{Challenge: challengeProto(req.GetChallengeId())}, nil
	}}
	c := newTestClient(t, srv)
	now := testStart
	c.now = func() time.Time { return now }

	for range 3 {
		if _, err := c.GetChallenge(context.Background(), "c1"); err != nil {
			t.Fatalf("GetChallenge: %v", err)
		}
	}
	if get, _ := srv.calls(); get != 1 {
		t.Fatalf("server calls = %d, want 1 (cache hit)", get)
	}

	now = now.Add(defaultCacheTTL)
	if _, err := c.GetChallenge(context.Background(), "c1"); err != nil {
		t.Fatalf("GetChallenge after TTL: %v", err)
	}
	if get, _ := srv.calls(); get != 2 {
		t.Errorf("server calls = %d, want 2 (entry expired)", get)
	}
}

func TestGetChallenge_ErrorsAreNotCached(t *testing.T) {
	srv := &mockChallengeServer{getResp: func(attempt int, req *internalv1.GetChallengeRequest) (*internalv1.GetChallengeResponse, error) {
		if attempt == 1 {
			return nil, statusWithReason(codes.NotFound, "CHALLENGE_NOT_FOUND")
		}
		return &internalv1.GetChallengeResponse{Challenge: challengeProto(req.GetChallengeId())}, nil
	}}
	c := newTestClient(t, srv)
	if _, err := c.GetChallenge(context.Background(), "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first call err = %v, want ErrNotFound", err)
	}
	if _, err := c.GetChallenge(context.Background(), "c1"); err != nil {
		t.Errorf("second call err = %v, want a fresh lookup", err)
	}
}

func TestInvoke_RetriesTransientCodes(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted} {
		t.Run(code.String(), func(t *testing.T) {
			srv := &mockChallengeServer{accResp: func(attempt int, _ *internalv1.CheckChallengeAccessRequest) (*internalv1.CheckChallengeAccessResponse, error) {
				if attempt < 3 {
					return nil, status.Error(code, "busy")
				}
				return &internalv1.CheckChallengeAccessResponse{Available: true}, nil
			}}
			got, err := newTestClient(t, srv).CheckChallengeAccess(context.Background(), "c1", "u1")
			if err != nil || !got.Available {
				t.Fatalf("CheckChallengeAccess = %+v, %v; want available after retries", got, err)
			}
			if _, acc := srv.calls(); acc != 3 {
				t.Errorf("attempts = %d, want 3", acc)
			}
		})
	}
}

func TestInvoke_GivesUpAfterMaxAttempts(t *testing.T) {
	srv := &mockChallengeServer{accResp: func(int, *internalv1.CheckChallengeAccessRequest) (*internalv1.CheckChallengeAccessResponse, error) {
		return nil, status.Error(codes.Unavailable, "down")
	}}
	_, err := newTestClient(t, srv).CheckChallengeAccess(context.Background(), "c1", "u1")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if _, acc := srv.calls(); acc != 3 {
		t.Errorf("attempts = %d, want 3", acc)
	}
}

func TestInvoke_DoesNotRetryPermanentErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"not found", statusWithReason(codes.NotFound, "CHALLENGE_NOT_FOUND"), ErrNotFound},
		{"invalid argument", statusWithReason(codes.InvalidArgument, "INVALID_REQUEST"), ErrInvalidArgument},
		{"bad secret", statusWithReason(codes.Unauthenticated, "INTERNAL_SECRET_INVALID"), ErrUnauthenticated},
		{"internal", statusWithReason(codes.Internal, "INTERNAL"), ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &mockChallengeServer{getResp: func(int, *internalv1.GetChallengeRequest) (*internalv1.GetChallengeResponse, error) {
				return nil, tc.err
			}}
			_, err := newTestClient(t, srv).GetChallenge(context.Background(), "c1")
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if get, _ := srv.calls(); get != 1 {
				t.Errorf("attempts = %d, want 1", get)
			}
		})
	}
}

func TestCheckChallengeAccess_MapsReasons(t *testing.T) {
	cases := []struct {
		in   *internalv1.CheckChallengeAccessResponse
		want Access
	}{
		{&internalv1.CheckChallengeAccessResponse{Available: true, Ended: true}, Access{Available: true, Ended: true}},
		{&internalv1.CheckChallengeAccessResponse{Reason: internalv1.AccessDeniedReason_ACCESS_DENIED_REASON_NOT_FOUND}, Access{Reason: AccessDeniedNotFound}},
		{&internalv1.CheckChallengeAccessResponse{Reason: internalv1.AccessDeniedReason_ACCESS_DENIED_REASON_DELETED}, Access{Reason: AccessDeniedDeleted}},
		{&internalv1.CheckChallengeAccessResponse{Reason: internalv1.AccessDeniedReason_ACCESS_DENIED_REASON_NOT_WHITELISTED}, Access{Reason: AccessDeniedNotWhitelisted}},
		{&internalv1.CheckChallengeAccessResponse{Reason: internalv1.AccessDeniedReason(42)}, Access{Reason: AccessDeniedUnknown}},
	}
	for _, tc := range cases {
		srv := &mockChallengeServer{accResp: func(_ int, req *internalv1.CheckChallengeAccessRequest) (*internalv1.CheckChallengeAccessResponse, error) {
			if req.GetChallengeId() != "c1" || req.GetUserId() != "u1" {
				t.Errorf("request = %v", req)
			}
			return tc.in, nil
		}}
		got, err := newTestClient(t, srv).CheckChallengeAccess(context.Background(), "c1", "u1")
		if err != nil || got != tc.want {
			t.Errorf("CheckChallengeAccess(%v) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
}

func TestInvoke_CallerCancelStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := &mockChallengeServer{accResp: func(int, *internalv1.CheckChallengeAccessRequest) (*internalv1.CheckChallengeAccessResponse, error) {
		cancel()
		return nil, status.Error(codes.Unavailable, "down")
	}}
	_, err := newTestClient(t, srv).CheckChallengeAccess(ctx, "c1", "u1")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, acc := srv.calls(); acc != 1 {
		t.Errorf("attempts = %d, want 1 after the caller cancelled", acc)
	}
}

func TestNew_RequiresAddrAndSecret(t *testing.T) {
	if _, err := New(context.Background(), Config{Secret: "s"}); err == nil {
		t.Error("New without Addr succeeded")
	}
	if _, err := New(context.Background(), Config{Addr: "dns:///x:1"}); err == nil {
		t.Error("New without Secret succeeded")
	}
}

func TestNew_DefaultDialIsLazy(t *testing.T) {
	c, err := New(context.Background(), Config{Addr: "dns:///challenger-internal-grpc.test.svc:7992", Secret: "s"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.CallTimeout != defaultCallTimeout || c.cfg.MaxAttempts != defaultMaxAttempts || c.cfg.Source != defaultSource {
		t.Errorf("defaults = %+v", c.cfg)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestChallengeCache_BoundedSize(t *testing.T) {
	cache := newChallengeCache(time.Minute, 2)
	now := testStart
	cache.set("a", Challenge{ID: "a"}, now)
	cache.set("b", Challenge{ID: "b"}, now)
	cache.set("c", Challenge{ID: "c"}, now)
	if n := cache.len(); n > 2 {
		t.Errorf("cache size = %d, want ≤ 2", n)
	}
	if _, ok := cache.get("c", now); !ok {
		t.Error("newest entry evicted")
	}
}
