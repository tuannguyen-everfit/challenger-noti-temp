// Package notification serves the in-app notification feed, unread summary, mark-read,
// push-device registry and account-deletion purge.
package notification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/Everfit-io/go-service-template/internal/infra/challengerclient"
	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/middleware"
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

// Platform is the OS a push token belongs to.
type Platform string

// Platform values.
const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
)

// AuditEntity is the kind of record an audit entry is about.
type AuditEntity string

// AuditEntity values.
const (
	AuditEntityNotification AuditEntity = "notification"
	AuditEntityDevice       AuditEntity = "device"
	AuditEntityUser         AuditEntity = "user" // account-deletion purge
)

// AuditAction is what happened to the record.
type AuditAction string

// AuditAction values.
const (
	AuditActionCreate AuditAction = "create"
	AuditActionUpdate AuditAction = "update"
	AuditActionDelete AuditAction = "delete"
	AuditActionPurge  AuditAction = "purge"
)

// Actor.Via values.
const (
	actorViaAPI         = "api"          // public API, Bearer-authed caller
	actorViaInternalAPI = "internal_api" // server-to-server route, Internal-Secret
)

// auditFieldMode says how an allow-listed field is recorded in AuditLog.Changes.
type auditFieldMode int

const (
	auditFieldValue  auditFieldMode = iota + 1 // from / to values
	auditFieldMasked                           // `changed: true` only — the value is a secret
)

// auditFields is the per-entity allow-list for AuditLog.Changes; anything else is dropped.
var auditFields = map[AuditEntity]map[string]auditFieldMode{
	AuditEntityNotification: {"read_at": auditFieldValue, "read_action": auditFieldValue, "buttons_hidden_at": auditFieldValue},
	AuditEntityDevice:       {"platform": auditFieldValue, "app_version": auditFieldValue, "token": auditFieldMasked},
}

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

	// WithTransaction runs fn in one transaction: Repo and AuditWriter calls made with fn's ctx
	// commit or roll back together. fn may run more than once (transient-error retry).
	WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error
	// FindDevice returns the user's row for deviceID; false when there is none.
	FindDevice(ctx context.Context, userID bson.ObjectID, deviceID string) (Device, bool, error)
	// FindDeviceByToken returns the row holding token (tokens are unique); false when there is none.
	FindDeviceByToken(ctx context.Context, token string) (Device, bool, error)
	// UpsertDevice writes d on (user_id, device_id) and returns the row _id; created_* apply on insert only.
	UpsertDevice(ctx context.Context, d Device) (bson.ObjectID, error)
	// TouchDevice moves last_registered_at and nothing else.
	TouchDevice(ctx context.Context, id bson.ObjectID, at time.Time) error
	// DeleteDevice removes the row; false when it was already gone.
	DeleteDevice(ctx context.Context, id bson.ObjectID) (bool, error)
	// FindNotification returns the user's row; false for an unknown id or another user's row.
	FindNotification(ctx context.Context, userID, id bson.ObjectID) (Notification, bool, error)
	// UpdateRead writes the non-nil fields of u plus updated_at / updated_by.
	UpdateRead(ctx context.Context, id bson.ObjectID, u readUpdate) error
	// MarkAllRead sets read_at / read_action = read_all on every unread row of the user; buttons stay.
	MarkAllRead(ctx context.Context, userID bson.ObjectID, at time.Time, by Actor) (int64, error)
	// DeleteNotificationsByUser hard-deletes every notification of the user and returns the count.
	DeleteNotificationsByUser(ctx context.Context, userID bson.ObjectID) (int64, error)
	// DeleteDevicesByUser hard-deletes every device of the user and returns the count.
	DeleteDevicesByUser(ctx context.Context, userID bson.ObjectID) (int64, error)
}

// AuditWriter appends entries to notification_audit_logs; call it with a WithTransaction ctx.
type AuditWriter interface {
	// Write inserts e; a duplicate audit_key returns errAuditRecorded.
	Write(ctx context.Context, e AuditLog) error
}

// challengeClient is the subset of challengerclient.Client that MarkRead depends on.
type challengeClient interface {
	GetChallenge(ctx context.Context, id string) (challengerclient.Challenge, error)
	CheckChallengeAccess(ctx context.Context, challengeID, userID string) (challengerclient.Access, error)
}

// Config holds the feature's operational knobs, mapped from platform config in app.go.
type Config struct {
	MaxListLimit int
}

// Service owns the feed, summary, read, device and purge paths.
type Service struct {
	repo       Repo
	audit      AuditWriter
	challenges challengeClient
	cfg        Config
	now        func() time.Time
}

// New builds the Service.
func New(repo Repo, audit AuditWriter, challenges challengeClient, cfg Config) *Service {
	return &Service{repo: repo, audit: audit, challenges: challenges, cfg: cfg, now: timex.Now}
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

// Device is one push target: a (user, device) pair holding one FCM token.
type Device struct {
	ID               bson.ObjectID `bson:"_id,omitempty"`
	UserID           bson.ObjectID `bson:"user_id"`
	DeviceID         string        `bson:"device_id"` // client-generated; the JWT carries no device claim
	Platform         Platform      `bson:"platform"`
	Token            string        `bson:"token"` // never logged or audited
	AppVersion       string        `bson:"app_version,omitempty"`
	LastRegisteredAt time.Time     `bson:"last_registered_at"` // every PUT; stale-token cleanup reads this, not updated_at
	CreatedBy        Actor         `bson:"created_by"`
	CreatedAt        time.Time     `bson:"created_at,omitempty"`
	UpdatedBy        *Actor        `bson:"updated_by,omitempty"`
	UpdatedAt        time.Time     `bson:"updated_at,omitempty"`
}

// AuditLog is one append-only entry in notification_audit_logs.
type AuditLog struct {
	ID        bson.ObjectID          `bson:"_id,omitempty"`
	Entity    AuditEntity            `bson:"entity"`
	EntityID  string                 `bson:"entity_id"` // "" for bulk entries
	UserID    *bson.ObjectID         `bson:"user_id,omitempty"`
	Action    AuditAction            `bson:"action"`
	Actor     Actor                  `bson:"actor"`
	Changes   map[string]FieldChange `bson:"changes,omitempty"`
	Count     int                    `bson:"count,omitempty"`
	Ref       string                 `bson:"ref,omitempty"`
	AuditKey  string                 `bson:"audit_key,omitempty"` // set by retried writers only
	RequestID string                 `bson:"request_id,omitempty"`
	TraceID   string                 `bson:"trace_id,omitempty"`
	At        time.Time              `bson:"at"`
}

// FieldChange is one allow-listed field's value before and after a write.
type FieldChange struct {
	From    any  `bson:"from,omitempty"`
	To      any  `bson:"to,omitempty"`
	Changed bool `bson:"changed,omitempty"` // replaces From / To for masked fields
}

// RegisterDeviceInput is the RegisterDevice request.
type RegisterDeviceInput struct {
	UserID     bson.ObjectID
	DeviceID   string
	Platform   Platform
	Token      string
	AppVersion string
}

// RemoveDeviceInput is the RemoveDevice request.
type RemoveDeviceInput struct {
	UserID   bson.ObjectID
	DeviceID string
}

// MarkReadInput is the MarkRead request; Action is tap, jump_in or nah.
type MarkReadInput struct {
	UserID bson.ObjectID
	ID     bson.ObjectID
	Action ReadAction
}

// MarkReadResult is the row after the read and the destination resolved now.
type MarkReadResult struct {
	Notification Notification
	Navigate     Navigate
	Available    bool // false: the challenge is gone or no longer visible to the user
}

// MarkAllReadInput is the MarkAllRead request.
type MarkAllReadInput struct {
	UserID bson.ObjectID
}

// readUpdate is what one mark-read writes; nil pointers leave the field as it is.
type readUpdate struct {
	ReadAt          *time.Time
	ReadAction      ReadAction
	ButtonsHiddenAt *time.Time
	UpdatedAt       time.Time
	UpdatedBy       Actor
}

// PurgeUserInput is the PurgeUser request; Caller is the calling service (`everfit-source`).
type PurgeUserInput struct {
	UserID bson.ObjectID
	Caller string
}

// PurgeUserResult counts the rows PurgeUser deleted.
type PurgeUserResult struct {
	Notifications int64
	Devices       int64
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

// RegisterDevice upserts the caller's device and evicts any other row holding the token, in one
// transaction with the audit entries. A PUT that changes nothing moves last_registered_at only.
func (s *Service) RegisterDevice(ctx context.Context, in RegisterDeviceInput) (Device, error) {
	now := s.now()
	var out Device
	err := s.runAudited(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.registerDevice(ctx, in, now)
		return err
	})
	if err != nil {
		return Device{}, fmt.Errorf("register device: %w", err)
	}
	return out, nil
}

// RemoveDevice deletes the caller's device; false (and no audit entry) when there is none.
func (s *Service) RemoveDevice(ctx context.Context, in RemoveDeviceInput) (bool, error) {
	now := s.now()
	var removed bool
	err := s.runAudited(ctx, func(ctx context.Context) error {
		removed = false
		current, found, err := s.repo.FindDevice(ctx, in.UserID, in.DeviceID)
		if err != nil || !found {
			return err
		}
		if removed, err = s.repo.DeleteDevice(ctx, current.ID); err != nil || !removed {
			return err
		}
		return s.recordAudit(ctx, buildDeviceAudit(current, AuditActionDelete, buildUserActor(in.UserID), nil, now))
	})
	if err != nil {
		return false, fmt.Errorf("remove device: %w", err)
	}
	return removed, nil
}

// MarkRead marks the caller's notification read (first read only) and hides its buttons, in one
// transaction with the audit entry, then resolves where the tap goes now. A re-tap writes
// nothing. The read is kept when challenger is unavailable; a retry resolves navigate.
func (s *Service) MarkRead(ctx context.Context, in MarkReadInput) (MarkReadResult, error) {
	now := s.now()
	actor := buildUserActor(in.UserID)
	var n Notification
	err := s.runAudited(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.FindNotification(ctx, in.UserID, in.ID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		n = cur
		u, changes := buildReadUpdate(cur, in.Action, actor, now)
		if len(changes) == 0 {
			return nil
		}
		if err := s.repo.UpdateRead(ctx, cur.ID, u); err != nil {
			return err
		}
		n = applyReadUpdate(cur, u)
		return s.recordAudit(ctx, buildNotificationAudit(cur, actor, changes, now))
	})
	if err != nil {
		return MarkReadResult{}, fmt.Errorf("mark read: %w", err)
	}
	nav, available, err := s.resolveNavigate(ctx, in.UserID, n.Navigate)
	if err != nil {
		return MarkReadResult{}, fmt.Errorf("mark read: %w", err)
	}
	return MarkReadResult{Notification: n, Navigate: nav, Available: available}, nil
}

// MarkAllRead marks every unread notification of the caller read with one audit entry; buttons stay.
func (s *Service) MarkAllRead(ctx context.Context, in MarkAllReadInput) (int64, error) {
	now := s.now()
	actor := buildUserActor(in.UserID)
	var updated int64
	err := s.runAudited(ctx, func(ctx context.Context) error {
		var err error
		if updated, err = s.repo.MarkAllRead(ctx, in.UserID, now, actor); err != nil {
			return err
		}
		owner := in.UserID
		return s.recordAudit(ctx, AuditLog{
			Entity: AuditEntityNotification, UserID: &owner, Action: AuditActionUpdate,
			Actor: actor, Count: int(updated), At: now,
		})
	})
	if err != nil {
		return 0, fmt.Errorf("mark all read: %w", err)
	}
	return updated, nil
}

// PurgeUser hard-deletes the user's notifications and devices with one `purge` entry, in one
// transaction. Idempotent: a repeat deletes nothing and writes no entry. Audit entries are kept.
func (s *Service) PurgeUser(ctx context.Context, in PurgeUserInput) (PurgeUserResult, error) {
	now := s.now()
	var out PurgeUserResult
	err := s.runAudited(ctx, func(ctx context.Context) error {
		var err error
		if out.Notifications, err = s.repo.DeleteNotificationsByUser(ctx, in.UserID); err != nil {
			return err
		}
		if out.Devices, err = s.repo.DeleteDevicesByUser(ctx, in.UserID); err != nil {
			return err
		}
		return s.recordAudit(ctx, buildPurgeAudit(in, out, now))
	})
	if err != nil {
		return PurgeUserResult{}, fmt.Errorf("purge user: %w", err)
	}
	return out, nil
}

// resolveNavigate re-checks the stored destination against challenger now: gone, deleted or no
// longer visible → none / unavailable; ended → leaderboard; otherwise challenge detail.
func (s *Service) resolveNavigate(ctx context.Context, userID bson.ObjectID, stored Navigate) (Navigate, bool, error) {
	if stored.Type == NavigateNone || stored.ChallengeID == "" {
		return Navigate{Type: NavigateNone}, true, nil
	}
	unavailable := Navigate{Type: NavigateNone}
	ch, err := s.challenges.GetChallenge(ctx, stored.ChallengeID)
	if errors.Is(err, challengerclient.ErrNotFound) {
		return unavailable, false, nil
	}
	if err != nil {
		return Navigate{}, false, s.translateChallengerErr(ctx, stored.ChallengeID, err)
	}
	if ch.Deleted {
		return unavailable, false, nil
	}
	ended := !ch.EndsAt.IsZero() && !s.now().Before(ch.EndsAt)
	// Only a published challenge is open to everyone; private (or a status this build doesn't know) needs the live check.
	if ch.Status != challengerclient.ChallengeStatusPublish {
		access, err := s.challenges.CheckChallengeAccess(ctx, stored.ChallengeID, userID.Hex())
		if err != nil {
			return Navigate{}, false, s.translateChallengerErr(ctx, stored.ChallengeID, err)
		}
		if !access.Available {
			return unavailable, false, nil
		}
		ended = access.Ended
	}
	if ended {
		return Navigate{Type: NavigateLeaderboard, ChallengeID: stored.ChallengeID}, true, nil
	}
	return Navigate{Type: NavigateChallengeDetail, ChallengeID: stored.ChallengeID}, true, nil
}

// translateChallengerErr turns an outage into ErrChallengerUnavailable (503); anything else is
// our bug or config (secret drift) and stays a 500.
func (s *Service) translateChallengerErr(ctx context.Context, challengeID string, err error) error {
	if errors.Is(err, challengerclient.ErrUnavailable) {
		log(ctx).Warn("challenger unavailable; navigate not resolved", slog.String("challenge_id", challengeID), slog.String("error", err.Error()))
		return fmt.Errorf("%w: %w", ErrChallengerUnavailable, err)
	}
	return fmt.Errorf("resolve navigate %s: %w", challengeID, err)
}

func (s *Service) registerDevice(ctx context.Context, in RegisterDeviceInput, now time.Time) (Device, error) {
	actor := buildUserActor(in.UserID)
	current, found, err := s.repo.FindDevice(ctx, in.UserID, in.DeviceID)
	if err != nil {
		return Device{}, err
	}
	changes := buildDeviceChanges(current, in)
	if found && len(changes) == 0 {
		if err := s.repo.TouchDevice(ctx, current.ID, now); err != nil {
			return Device{}, err
		}
		current.LastRegisteredAt = now
		return current, nil
	}
	// The token index is unique, so the old holder must go before the upsert.
	if err := s.evictToken(ctx, in, actor, now); err != nil {
		return Device{}, err
	}

	next := Device{
		ID: current.ID, UserID: in.UserID, DeviceID: in.DeviceID, Platform: in.Platform, Token: in.Token,
		AppVersion: in.AppVersion, LastRegisteredAt: now, CreatedBy: actor, CreatedAt: now, UpdatedBy: &actor, UpdatedAt: now,
	}
	if next.ID, err = s.repo.UpsertDevice(ctx, next); err != nil {
		return Device{}, err
	}
	action := AuditActionCreate
	if found {
		next.CreatedBy, next.CreatedAt = current.CreatedBy, current.CreatedAt
		action = AuditActionUpdate
	} else {
		changes = nil
	}
	if err := s.recordAudit(ctx, buildDeviceAudit(next, action, actor, changes, now)); err != nil {
		return Device{}, err
	}
	return next, nil
}

// evictToken deletes the row holding in.Token when it isn't the caller's (user, device) row.
func (s *Service) evictToken(ctx context.Context, in RegisterDeviceInput, actor Actor, now time.Time) error {
	holder, found, err := s.repo.FindDeviceByToken(ctx, in.Token)
	if err != nil || !found || (holder.UserID == in.UserID && holder.DeviceID == in.DeviceID) {
		return err
	}
	deleted, err := s.repo.DeleteDevice(ctx, holder.ID)
	if err != nil || !deleted {
		return err
	}
	return s.recordAudit(ctx, buildDeviceAudit(holder, AuditActionDelete, actor, nil, now))
}

// runAudited runs fn in one transaction. errAuditRecorded means a retried write that already
// committed, so it is success; only writers that set AuditKey (and return nothing) can hit it.
func (s *Service) runAudited(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := s.repo.WithTransaction(ctx, fn); err != nil && !errors.Is(err, errAuditRecorded) {
		return err
	}
	return nil
}

// recordAudit writes e inside the caller's transaction with the allow-listed changes and the
// request / trace ids from ctx. A no-op (see auditIsNoOp) writes no entry.
func (s *Service) recordAudit(ctx context.Context, e AuditLog) error {
	e.Changes = pickAuditChanges(e.Entity, e.Changes)
	if auditIsNoOp(e) {
		return nil
	}
	e.RequestID = middleware.RequestIDFromContext(ctx)
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		e.TraceID = sc.TraceID().String()
	}
	if err := s.audit.Write(ctx, e); err != nil {
		return fmt.Errorf("write %s %s audit: %w", e.Entity, e.Action, err)
	}
	return nil
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

// buildReadUpdate lists what a read changes on n: read_at + read_action on the first read only,
// buttons_hidden_at when buttons are still shown. No changes means nothing to write.
func buildReadUpdate(n Notification, action ReadAction, actor Actor, now time.Time) (readUpdate, map[string]FieldChange) {
	u := readUpdate{UpdatedAt: now, UpdatedBy: actor}
	changes := map[string]FieldChange{}
	if n.ReadAt == nil {
		u.ReadAt, u.ReadAction = &now, action
		changes["read_at"] = FieldChange{To: now}
		changes["read_action"] = FieldChange{To: action}
	}
	if len(n.Buttons) > 0 && n.ButtonsHiddenAt == nil {
		u.ButtonsHiddenAt = &now
		changes["buttons_hidden_at"] = FieldChange{To: now}
	}
	return u, changes
}

func applyReadUpdate(n Notification, u readUpdate) Notification {
	if u.ReadAt != nil {
		n.ReadAt, n.ReadAction = u.ReadAt, u.ReadAction
	}
	if u.ButtonsHiddenAt != nil {
		n.ButtonsHiddenAt = u.ButtonsHiddenAt
	}
	by := u.UpdatedBy
	n.UpdatedAt, n.UpdatedBy = u.UpdatedAt, &by
	return n
}

func buildNotificationAudit(n Notification, actor Actor, changes map[string]FieldChange, at time.Time) AuditLog {
	owner := n.UserID
	return AuditLog{
		Entity:   AuditEntityNotification,
		EntityID: n.ID.Hex(),
		UserID:   &owner,
		Action:   AuditActionUpdate,
		Actor:    actor,
		Changes:  changes,
		At:       at,
	}
}

func buildUserActor(userID bson.ObjectID) Actor {
	return Actor{Type: ActorTypeUser, ID: userID.Hex(), Via: actorViaAPI}
}

// buildDeviceChanges lists the audited fields that in changes on prev.
func buildDeviceChanges(prev Device, in RegisterDeviceInput) map[string]FieldChange {
	changes := map[string]FieldChange{}
	if prev.Platform != in.Platform {
		changes["platform"] = FieldChange{From: prev.Platform, To: in.Platform}
	}
	if prev.AppVersion != in.AppVersion {
		changes["app_version"] = FieldChange{From: prev.AppVersion, To: in.AppVersion}
	}
	if prev.Token != in.Token {
		changes["token"] = FieldChange{Changed: true}
	}
	return changes
}

func buildDeviceAudit(d Device, action AuditAction, actor Actor, changes map[string]FieldChange, at time.Time) AuditLog {
	owner := d.UserID
	return AuditLog{
		Entity:   AuditEntityDevice,
		EntityID: d.ID.Hex(),
		UserID:   &owner,
		Action:   action,
		Actor:    actor,
		Changes:  changes,
		At:       at,
	}
}

func buildPurgeAudit(in PurgeUserInput, res PurgeUserResult, at time.Time) AuditLog {
	owner := in.UserID
	return AuditLog{
		Entity:   AuditEntityUser,
		EntityID: in.UserID.Hex(),
		UserID:   &owner,
		Action:   AuditActionPurge,
		Actor:    Actor{Type: ActorTypeService, ID: in.Caller, Via: actorViaInternalAPI},
		Count:    int(res.Notifications + res.Devices),
		At:       at,
	}
}

// auditIsNoOp reports an entry that records nothing: an update without an allow-listed change
// or a count, or a purge that deleted no row.
func auditIsNoOp(e AuditLog) bool {
	switch e.Action {
	case AuditActionUpdate:
		return len(e.Changes) == 0 && e.Count == 0
	case AuditActionPurge:
		return e.Count == 0
	case AuditActionCreate, AuditActionDelete:
		return false
	}
	return false
}

// pickAuditChanges keeps the entity's allow-listed fields; masked fields keep only `changed: true`.
func pickAuditChanges(entity AuditEntity, changes map[string]FieldChange) map[string]FieldChange {
	allowed := auditFields[entity]
	out := make(map[string]FieldChange, len(changes))
	for field, c := range changes {
		switch allowed[field] {
		case auditFieldValue:
			out[field] = FieldChange{From: c.From, To: c.To}
		case auditFieldMasked:
			out[field] = FieldChange{Changed: true}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
