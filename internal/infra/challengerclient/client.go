// Package challengerclient is the typed gRPC client for challenger-service's
// challenger.internal.v1 read API. It owns the dial (round-robin over the
// headless Service), the internal-api-secret metadata, the per-attempt
// timeout, retries on transient codes, ErrorInfo → sentinel translation and a
// short GetChallenge cache. infra never imports platform/apperr: callers map
// the sentinels in errors.go at their own boundary.
package challengerclient

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Everfit-io/go-service-template/internal/infra/grpcclient"
	"github.com/Everfit-io/go-service-template/internal/stdx/retry"
	"github.com/Everfit-io/go-service-template/proto/challenger/internalv1"
)

// ChallengeStatus is the challenge's CMS status; anything unexpected is Unknown.
type ChallengeStatus string

// ChallengeStatus values.
const (
	ChallengeStatusPrivate ChallengeStatus = "private"
	ChallengeStatusPublish ChallengeStatus = "publish"
	ChallengeStatusUnknown ChallengeStatus = "unknown" // unset or a newer server's value
)

// AccessDeniedReason says why CheckChallengeAccess denied; "" when available.
type AccessDeniedReason string

// AccessDeniedReason values.
const (
	AccessDeniedNotFound       AccessDeniedReason = "not_found"
	AccessDeniedDeleted        AccessDeniedReason = "deleted"
	AccessDeniedNotWhitelisted AccessDeniedReason = "not_whitelisted"
	AccessDeniedUnknown        AccessDeniedReason = "unknown" // a newer server's value
)

// metadata keys sent on every RPC; the server checks internal-api-secret.
const (
	metadataSecret = "internal-api-secret"
	metadataSource = "everfit-source"
)

const (
	defaultCallTimeout  = 2 * time.Second
	defaultMaxAttempts  = 3
	defaultRetryBackoff = 200 * time.Millisecond
	defaultCacheTTL     = 5 * time.Minute
	defaultSource       = "challenger-notification-api"
	// cacheMaxEntries bounds the GetChallenge cache; live challenges are single digits.
	cacheMaxEntries = 1024
)

// roundRobinServiceConfig spreads RPCs over every pod behind the headless Service (design §4.5).
const roundRobinServiceConfig = `{"loadBalancingConfig":[{"round_robin":{}}]}`

// Config carries dial-time and call-time settings; Addr and Secret are required.
type Config struct {
	Addr         string            // e.g. dns:///challenger-internal-grpc.<ns>.svc.cluster.local:7992
	Secret       string            // = challenger's CHALLENGER_GRPC_INTERNAL_SECRET; never logged
	Source       string            // everfit-source metadata; default challenger-notification-api
	CallTimeout  time.Duration     // per attempt; default 2s
	MaxAttempts  int               // including the first; default 3
	RetryBackoff time.Duration     // first backoff, doubled per retry; default 200ms
	CacheTTL     time.Duration     // GetChallenge cache; default 5m
	DialOptions  []grpc.DialOption // tests only (bufconn); production uses grpcclient.Dial
}

// Client holds one connection and the typed stubs; build once at boot, share across requests.
type Client struct {
	conn       *grpc.ClientConn
	challenges internalv1.ChallengeInternalServiceClient
	cfg        Config
	cache      *challengeCache
	now        func() time.Time
}

// Challenge is challenger's challenge as the notification service sees it.
type Challenge struct {
	ID               string
	Name             string
	Status           ChallengeStatus
	StartsAt         time.Time // zero when unset
	EndsAt           time.Time // zero when unset
	ThumbnailURL     string
	ShortDescription string
	Tagline          string
	Deleted          bool
}

// Access is CheckChallengeAccess's answer for one (challenge, user).
type Access struct {
	Available bool
	Reason    AccessDeniedReason
	Ended     bool // now ≥ ends_at, reported whatever Available is
}

// New dials challenger lazily (the first RPC connects). The caller owns Close.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, errors.New("challengerclient: Addr is required")
	}
	if cfg.Secret == "" {
		return nil, errors.New("challengerclient: Secret is required")
	}
	cfg = withDefaults(cfg)

	var (
		conn *grpc.ClientConn
		err  error
	)
	if len(cfg.DialOptions) > 0 {
		conn, err = grpc.NewClient(cfg.Addr, cfg.DialOptions...)
	} else {
		conn, err = grpcclient.Dial(ctx, cfg.Addr, grpcclient.WithDialOption(grpc.WithDefaultServiceConfig(roundRobinServiceConfig)))
	}
	if err != nil {
		return nil, fmt.Errorf("challengerclient: dial %s: %w", cfg.Addr, err)
	}
	return &Client{
		conn:       conn,
		challenges: internalv1.NewChallengeInternalServiceClient(conn),
		cfg:        cfg,
		cache:      newChallengeCache(cfg.CacheTTL, cacheMaxEntries),
		now:        time.Now,
	}, nil
}

// Close releases the connection; safe to call twice.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// GetChallenge returns the challenge in any status, including deleted; ErrNotFound when the id
// never existed. Successful answers are cached for CacheTTL.
func (c *Client) GetChallenge(ctx context.Context, id string) (Challenge, error) {
	if ch, ok := c.cache.get(id, c.now()); ok {
		return ch, nil
	}
	resp, err := invoke(ctx, c, func(ctx context.Context) (*internalv1.GetChallengeResponse, error) {
		return c.challenges.GetChallenge(ctx, &internalv1.GetChallengeRequest{ChallengeId: id})
	})
	if err != nil {
		return Challenge{}, err
	}
	ch := toChallenge(resp.GetChallenge())
	c.cache.set(id, ch, c.now())
	return ch, nil
}

// CheckChallengeAccess reports whether userID may open the challenge right now; never cached.
func (c *Client) CheckChallengeAccess(ctx context.Context, challengeID, userID string) (Access, error) {
	resp, err := invoke(ctx, c, func(ctx context.Context) (*internalv1.CheckChallengeAccessResponse, error) {
		return c.challenges.CheckChallengeAccess(ctx, &internalv1.CheckChallengeAccessRequest{ChallengeId: challengeID, UserId: userID})
	})
	if err != nil {
		return Access{}, err
	}
	return Access{Available: resp.GetAvailable(), Reason: toAccessDeniedReason(resp.GetReason()), Ended: resp.GetEnded()}, nil
}

// invoke is the one seam every RPC shares: metadata, per-attempt deadline, retry on transient
// codes, then status → sentinel translation.
func invoke[Resp any](ctx context.Context, c *Client, fn func(ctx context.Context) (Resp, error)) (Resp, error) {
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(metadataSecret, c.cfg.Secret, metadataSource, c.cfg.Source))
	resp, err := retry.DoWithResult(ctx, func() (Resp, error) {
		callCtx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
		defer cancel()
		return fn(callCtx)
	},
		retry.WithMaxAttempts(c.cfg.MaxAttempts),
		retry.WithInitialBackoff(c.cfg.RetryBackoff),
		retry.WithRetryIf(isRetryable),
	)
	if err != nil {
		var zero Resp
		return zero, translateErr(ctx, err)
	}
	return resp, nil
}

func withDefaults(cfg Config) Config {
	if cfg.Source == "" {
		cfg.Source = defaultSource
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = defaultCallTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRetryBackoff
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultCacheTTL
	}
	return cfg
}

// retryableCodes are the transient codes of design §3; every RPC here is a read, so all are safe.
var retryableCodes = []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted}

func isRetryable(err error) bool { return slices.Contains(retryableCodes, status.Code(err)) }

func toChallenge(p *internalv1.Challenge) Challenge {
	ch := Challenge{
		ID:               p.GetId(),
		Name:             p.GetName(),
		Status:           toChallengeStatus(p.GetStatus()),
		ThumbnailURL:     p.GetThumbnailUrl(),
		ShortDescription: p.GetShortDescription(),
		Tagline:          p.GetTagline(),
		Deleted:          p.GetDeleted(),
	}
	if p.GetStartsAt() != nil {
		ch.StartsAt = p.GetStartsAt().AsTime()
	}
	if p.GetEndsAt() != nil {
		ch.EndsAt = p.GetEndsAt().AsTime()
	}
	return ch
}

func toChallengeStatus(s internalv1.ChallengeStatus) ChallengeStatus {
	switch s {
	case internalv1.ChallengeStatus_CHALLENGE_STATUS_PRIVATE:
		return ChallengeStatusPrivate
	case internalv1.ChallengeStatus_CHALLENGE_STATUS_PUBLISH:
		return ChallengeStatusPublish
	case internalv1.ChallengeStatus_CHALLENGE_STATUS_UNSPECIFIED:
		return ChallengeStatusUnknown
	default:
		return ChallengeStatusUnknown
	}
}

func toAccessDeniedReason(r internalv1.AccessDeniedReason) AccessDeniedReason {
	switch r {
	case internalv1.AccessDeniedReason_ACCESS_DENIED_REASON_UNSPECIFIED:
		return ""
	case internalv1.AccessDeniedReason_ACCESS_DENIED_REASON_NOT_FOUND:
		return AccessDeniedNotFound
	case internalv1.AccessDeniedReason_ACCESS_DENIED_REASON_DELETED:
		return AccessDeniedDeleted
	case internalv1.AccessDeniedReason_ACCESS_DENIED_REASON_NOT_WHITELISTED:
		return AccessDeniedNotWhitelisted
	default:
		return AccessDeniedUnknown
	}
}
