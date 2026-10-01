// Package notification serves the in-app notification feed, unread summary and push-device registry.
package notification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

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
	AuditEntityDevice AuditEntity = "device"
)

// AuditAction is what happened to the record.
type AuditAction string

// AuditAction values.
const (
	AuditActionCreate AuditAction = "create"
	AuditActionUpdate AuditAction = "update"
	AuditActionDelete AuditAction = "delete"
)

// actorViaAPI marks a write made through the public API (Actor.Via).
const actorViaAPI = "api"

// auditFieldMode says how an allow-listed field is recorded in AuditLog.Changes.
type auditFieldMode int

const (
	auditFieldValue  auditFieldMode = iota + 1 // from / to values
	auditFieldMasked                           // `changed: true` only — the value is a secret
)

// auditFields is the per-entity allow-list for AuditLog.Changes; anything else is dropped.
var auditFields = map[AuditEntity]map[string]auditFieldMode{
	AuditEntityDevice: {"platform": auditFieldValue, "app_version": auditFieldValue, "token": auditFieldMasked},
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
}

// AuditWriter appends entries to notification_audit_logs; call it with a WithTransaction ctx.
type AuditWriter interface {
	// Write inserts e; a duplicate audit_key returns errAuditRecorded.
	Write(ctx context.Context, e AuditLog) error
}

// Config holds the feature's operational knobs, mapped from platform config in app.go.
type Config struct {
	MaxListLimit int
}

// Service owns the feed, summary and device paths.
type Service struct {
	repo  Repo
	audit AuditWriter
	cfg   Config
	now   func() time.Time
}

// New builds the Service.
func New(repo Repo, audit AuditWriter, cfg Config) *Service {
	return &Service{repo: repo, audit: audit, cfg: cfg, now: timex.Now}
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
// request / trace ids from ctx. An update with nothing left to record writes no entry.
func (s *Service) recordAudit(ctx context.Context, e AuditLog) error {
	e.Changes = pickAuditChanges(e.Entity, e.Changes)
	if e.Action == AuditActionUpdate && len(e.Changes) == 0 && e.Count == 0 {
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
