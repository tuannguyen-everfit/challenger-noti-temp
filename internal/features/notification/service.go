// Package notification serves the in-app notification feed and unread summary.
package notification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/sync/errgroup"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
	"github.com/Everfit-io/go-service-template/internal/platform/pagination"
	"github.com/Everfit-io/go-service-template/internal/stdx/safego"
	"github.com/Everfit-io/go-service-template/internal/stdx/timex"
)

// Kind is the notification type; the value is the wire + storage string.
type Kind string

// Kind values; the N-label is the Confluence requirement.
const (
	KindFriendInvite        Kind = "friend_invite"         // N1
	KindChallengePublished  Kind = "challenge_published"   // N2
	KindChallengeComingSoon Kind = "challenge_coming_soon" // N3
	KindPersonalBest        Kind = "personal_best"         // N4
	KindChallengeNudge      Kind = "challenge_nudge"       // N5
	KindFriendJoined        Kind = "friend_joined"         // N6
	KindChallengeEnding     Kind = "challenge_ending"      // N7
	KindChallengeEnded      Kind = "challenge_ended"       // N8
)

// Tab is the feed tab a kind belongs to.
type Tab string

// Tab values.
const (
	TabAll        Tab = "all" // query-only: no tab filter; never stored
	TabActivities Tab = "activities"
	TabSystem     Tab = "system"
)

// ImageType is what the card image shows.
type ImageType string

// ImageType values.
const (
	ImageTypeAvatar    ImageType = "avatar"
	ImageTypeChallenge ImageType = "challenge"
	ImageTypeApp       ImageType = "app"
)

// NavigateType is where the first tap on a card goes.
type NavigateType string

// NavigateType values.
const (
	NavigateChallengeDetail NavigateType = "challenge_detail"
	NavigateLeaderboard     NavigateType = "leaderboard"
	NavigateNone            NavigateType = "none"
)

// Button is an action button on a card.
type Button string

// Button values.
const (
	ButtonJumpIn Button = "jump_in"
	ButtonNah    Button = "nah"
)

// ActorType identifies who performed a write.
type ActorType string

// ActorType values.
const (
	ActorTypeUser    ActorType = "user"
	ActorTypeSystem  ActorType = "system"
	ActorTypeService ActorType = "service"
)

// SourceType is why a notification row exists.
type SourceType string

// SourceType values.
const (
	SourceTypeEvent     SourceType = "event"
	SourceTypeBroadcast SourceType = "broadcast"
)

// ReadAction records how a notification was marked read.
type ReadAction string

// ReadAction values.
const (
	ReadActionTap     ReadAction = "tap"
	ReadActionJumpIn  ReadAction = "jump_in"
	ReadActionNah     ReadAction = "nah"
	ReadActionReadAll ReadAction = "read_all"
)

// summaryCountCap bounds every summary count: clients render ≥ 100 as "99+".
const summaryCountCap = 100

// thisWeekDays is "the previous 6 days" before today (AC 0.4).
const thisWeekDays = 6

// Repo is the notifications persistence port.
type Repo interface {
	// ListFeed returns ≤ limit rows newest first; TabAll = no tab filter, zero after = first page.
	ListFeed(ctx context.Context, userID bson.ObjectID, tab Tab, after feedCursor, limit int) ([]Notification, error)
	// CountUnread returns ≤ summaryCountCap unread rows in tab.
	CountUnread(ctx context.Context, userID bson.ObjectID, tab Tab) (int64, error)
	// CountRange returns ≤ summaryCountCap rows with activity_at in [from, to); a zero bound is unbounded.
	CountRange(ctx context.Context, userID bson.ObjectID, tab Tab, from, to time.Time) (int64, error)
}

// Config holds the feature's operational knobs, mapped from platform config in app.go.
type Config struct {
	MaxListLimit int
}

// Service owns the feed and summary read paths.
type Service struct {
	repo Repo
	cfg  Config
	now  func() time.Time
}

// New builds the Service.
func New(repo Repo, cfg Config) *Service {
	return &Service{repo: repo, cfg: cfg, now: timex.Now}
}

// Notification is one card in a user's feed.
type Notification struct {
	ID              bson.ObjectID  `bson:"_id,omitempty"`
	UserID          bson.ObjectID  `bson:"user_id"`
	Kind            Kind           `bson:"kind"`
	Tab             Tab            `bson:"tab"`
	ChallengeID     *bson.ObjectID `bson:"challenge_id,omitempty"`
	DedupeKey       string         `bson:"dedupe_key,omitempty"`
	StackKey        string         `bson:"stack_key,omitempty"`
	Stack           *Stack         `bson:"stack,omitempty"`
	Title           string         `bson:"title"`
	Body            string         `bson:"body"`
	Image           Image          `bson:"image"`
	Navigate        Navigate       `bson:"navigate"`
	Buttons         []Button       `bson:"buttons"`
	ButtonsHiddenAt *time.Time     `bson:"buttons_hidden_at,omitempty"`
	ReadAt          *time.Time     `bson:"read_at,omitempty"`
	ReadAction      ReadAction     `bson:"read_action,omitempty"`
	Push            PushState      `bson:"push"`
	Source          Source         `bson:"source"`
	SchemaVersion   int            `bson:"schema_version"`
	ActivityAt      time.Time      `bson:"activity_at"`
	CreatedBy       Actor          `bson:"created_by"`
	CreatedAt       time.Time      `bson:"created_at,omitempty"`
	UpdatedBy       *Actor         `bson:"updated_by,omitempty"`
	UpdatedAt       time.Time      `bson:"updated_at,omitempty"`
}

// Stack groups repeated items of a stackable kind into one card.
type Stack struct {
	Actors          []StackActor `bson:"actors"` // newest first
	Count           int          `bson:"count"`
	WindowStartedAt time.Time    `bson:"window_started_at"`
}

// StackActor is one user shown on a stacked card.
type StackActor struct {
	UserID      bson.ObjectID `bson:"user_id"`
	FirstName   string        `bson:"first_name"`
	LastInitial string        `bson:"last_initial"`
	AvatarURL   string        `bson:"avatar_url"`
}

// Actor is who performed a write (`created_by` / `updated_by`).
type Actor struct {
	Type ActorType `bson:"type"`
	ID   string    `bson:"id,omitempty"`
	Via  string    `bson:"via,omitempty"`
}

// Image is the card image.
type Image struct {
	Type     ImageType `bson:"type"`
	URL      string    `bson:"url"`
	BadgeURL string    `bson:"badge_url,omitempty"`
}

// Navigate is the first-tap destination.
type Navigate struct {
	Type        NavigateType `bson:"type"`
	ChallengeID string       `bson:"challenge_id,omitempty"`
}

// Source records why the row exists.
type Source struct {
	Type          SourceType     `bson:"type"`
	EventID       string         `bson:"event_id,omitempty"`
	DispatchID    *bson.ObjectID `bson:"dispatch_id,omitempty"`
	WorkflowRunID string         `bson:"workflow_run_id,omitempty"`
}

// PushState is the push-delivery outcome for the row.
type PushState struct {
	SentAt        *time.Time `bson:"sent_at,omitempty"`
	SentCount     int        `bson:"sent_count"`
	FailedCount   int        `bson:"failed_count"`
	Error         string     `bson:"error,omitempty"`
	SkippedReason string     `bson:"skipped_reason,omitempty"`
}

// feedCursor is the feed position over (activity_at DESC, _id DESC).
type feedCursor struct {
	ActivityAt time.Time
	ID         bson.ObjectID
}

type feedCursorWire struct {
	ActivityAt time.Time `json:"a"`
	ID         string    `json:"i"`
}

// ListFeedInput is the ListFeed request.
type ListFeedInput struct {
	UserID bson.ObjectID
	Tab    Tab
	After  feedCursor
	Limit  int
}

// ListFeedResult is one feed page; Items is never nil.
type ListFeedResult struct {
	Items      []Notification
	NextCursor string
	HasMore    bool
}

// SummaryInput is the Summary request; Location sets the day boundaries.
type SummaryInput struct {
	UserID   bson.ObjectID
	Tab      Tab
	Location *time.Location
}

// SummaryResult holds unread counts (both tabs) and group counts (the requested tab).
type SummaryResult struct {
	UnreadTotal      int64
	UnreadActivities int64
	UnreadSystem     int64
	Today            int64
	ThisWeek         int64
	Earlier          int64
}

// IsZero reports whether the cursor is the start of the feed.
func (c feedCursor) IsZero() bool { return c.ID.IsZero() && c.ActivityAt.IsZero() }

// ListFeed returns one page of the caller's feed.
func (s *Service) ListFeed(ctx context.Context, in ListFeedInput) (ListFeedResult, error) {
	in.Limit = s.computeListLimit(in.Limit)
	if err := s.validateListLimit(in.Limit); err != nil {
		return ListFeedResult{}, err
	}
	rows, err := s.repo.ListFeed(ctx, in.UserID, in.Tab, in.After, in.Limit+1)
	if err != nil {
		return ListFeedResult{}, fmt.Errorf("list feed: %w", err)
	}
	return buildFeedPage(rows, in.Limit), nil
}

// Summary returns the badge, tab-dot and group counts, each capped at summaryCountCap.
func (s *Service) Summary(ctx context.Context, in SummaryInput) (SummaryResult, error) {
	todayStart := timex.StartOfDay(s.now(), in.Location)
	weekStart := todayStart.AddDate(0, 0, -thisWeekDays)

	// Each goroutine writes its own field, so no lock is needed.
	var out SummaryResult
	g, gctx := errgroup.WithContext(ctx)
	g.Go(safego.WrapErr(func() (err error) {
		out.UnreadActivities, err = s.repo.CountUnread(gctx, in.UserID, TabActivities)
		return err
	}))
	g.Go(safego.WrapErr(func() (err error) {
		out.UnreadSystem, err = s.repo.CountUnread(gctx, in.UserID, TabSystem)
		return err
	}))
	g.Go(safego.WrapErr(func() (err error) {
		out.Today, err = s.repo.CountRange(gctx, in.UserID, in.Tab, todayStart, time.Time{})
		return err
	}))
	g.Go(safego.WrapErr(func() (err error) {
		out.ThisWeek, err = s.repo.CountRange(gctx, in.UserID, in.Tab, weekStart, todayStart)
		return err
	}))
	g.Go(safego.WrapErr(func() (err error) {
		out.Earlier, err = s.repo.CountRange(gctx, in.UserID, in.Tab, time.Time{}, weekStart)
		return err
	}))
	if err := g.Wait(); err != nil {
		return SummaryResult{}, fmt.Errorf("summary: %w", err)
	}
	out.UnreadTotal = out.UnreadActivities + out.UnreadSystem
	return out, nil
}

func (s *Service) computeListLimit(limit int) int {
	if limit > 0 {
		return limit
	}
	if s.cfg.MaxListLimit > 0 && s.cfg.MaxListLimit < pagination.DefaultLimit {
		return s.cfg.MaxListLimit
	}
	return pagination.DefaultLimit
}

func (s *Service) validateListLimit(limit int) error {
	if s.cfg.MaxListLimit > 0 && limit > s.cfg.MaxListLimit {
		return apperr.BadRequest(localization.CodeInvalidRequest, fmt.Sprintf("limit must be ≤ %d", s.cfg.MaxListLimit))
	}
	return nil
}

func buildFeedPage(rows []Notification, limit int) ListFeedResult {
	if len(rows) <= limit {
		if rows == nil {
			rows = []Notification{}
		}
		return ListFeedResult{Items: rows}
	}
	rows = rows[:limit]
	last := rows[len(rows)-1]
	return ListFeedResult{
		Items:      rows,
		NextCursor: feedCursor{ActivityAt: last.ActivityAt, ID: last.ID}.encode(),
		HasMore:    true,
	}
}

// encode returns "" for the zero cursor; ActivityAt is truncated to ms (Mongo date precision).
func (c feedCursor) encode() string {
	if c.IsZero() {
		return ""
	}
	b, err := json.Marshal(feedCursorWire{ActivityAt: c.ActivityAt.Truncate(time.Millisecond).UTC(), ID: c.ID.Hex()})
	if err != nil {
		// Fixed-shape struct of public types — Marshal cannot fail.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseFeedCursor decodes an opaque cursor; "" is the first page.
func parseFeedCursor(s string) (feedCursor, error) {
	if s == "" {
		return feedCursor{}, nil
	}
	invalid := apperr.BadRequest(localization.CodeInvalidRequest, "invalid cursor")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return feedCursor{}, invalid.Wrap(err)
	}
	var w feedCursorWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return feedCursor{}, invalid.Wrap(err)
	}
	id, err := bson.ObjectIDFromHex(w.ID)
	if err != nil {
		return feedCursor{}, invalid.Wrap(err)
	}
	if id.IsZero() || w.ActivityAt.IsZero() {
		return feedCursor{}, invalid
	}
	return feedCursor{ActivityAt: w.ActivityAt.Truncate(time.Millisecond).UTC(), ID: id}, nil
}

// parseTimezone maps `tz` to a location; "" is UTC, and only IANA names are accepted.
func parseTimezone(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	if name == "Local" {
		return nil, ErrInvalidTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTimezone, err)
	}
	return loc, nil
}
