package notification

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/middleware"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/typed"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

type notificationService interface {
	ListFeed(ctx context.Context, in ListFeedInput) (ListFeedResult, error)
	Summary(ctx context.Context, in SummaryInput) (SummaryResult, error)
	RegisterDevice(ctx context.Context, in RegisterDeviceInput) (Device, error)
	RemoveDevice(ctx context.Context, in RemoveDeviceInput) (bool, error)
	PurgeUser(ctx context.Context, in PurgeUserInput) (PurgeUserResult, error)
}

// Handler serves the notification HTTP endpoints.
type Handler struct{ svc notificationService }

// NewHandler builds the Handler.
func NewHandler(svc notificationService) *Handler { return &Handler{svc: svc} }

// Routes returns the /notifications sub-router; every route requires Bearer auth.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", typed.JSON(http.StatusOK, h.List))
	r.Get("/summary", typed.JSON(http.StatusOK, h.Summary))
	return r
}

// DeviceRoutes returns the /devices sub-router; every route requires Bearer auth.
func (h *Handler) DeviceRoutes() chi.Router {
	r := chi.NewRouter()
	r.Put("/{device_id}", typed.JSON(http.StatusOK, h.RegisterDevice))
	r.Delete("/{device_id}", typed.JSON(http.StatusOK, h.RemoveDevice))
	return r
}

// RegisterInternalRoutes adds the server-to-server routes; the router guards them with Internal-Secret.
func (h *Handler) RegisterInternalRoutes(r chi.Router) {
	r.Delete("/internal/notifications/users/{user_id}", typed.JSON(http.StatusOK, h.PurgeUser))
}

// ListRequest is GET /notifications.
type ListRequest struct {
	Tab    string `query:"tab"    validate:"omitempty,oneof=all activities system"`
	Cursor string `query:"cursor" validate:"omitempty,max=512"`
	Limit  int    `query:"limit"  validate:"omitempty,min=1"` // 0 → Service default; ceiling enforced by Service
}

// SummaryRequest is GET /notifications/summary.
type SummaryRequest struct {
	Tab string `query:"tab" validate:"omitempty,oneof=all activities system"`
	TZ  string `query:"tz"  validate:"omitempty,max=64"`
}

// RegisterDeviceRequest is PUT /devices/{device_id}.
type RegisterDeviceRequest struct {
	DeviceID   string `path:"device_id"             validate:"required,max=128"`
	Platform   string `json:"platform"              validate:"required,oneof=ios android"`
	Token      string `json:"token"                 validate:"required,max=4096"`
	AppVersion string `json:"app_version,omitempty" validate:"omitempty,max=64"`
}

// RemoveDeviceRequest is DELETE /devices/{device_id}.
type RemoveDeviceRequest struct {
	DeviceID string `path:"device_id" validate:"required,max=128"`
}

// DeviceResponse is the registered device on the wire; the token is never echoed.
type DeviceResponse struct {
	DeviceID  string    `json:"device_id"`
	Platform  string    `json:"platform"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RemoveDeviceResponse reports whether a device row was deleted.
type RemoveDeviceResponse struct {
	Removed bool `json:"removed"`
}

// PurgeUserRequest is DELETE /internal/notifications/users/{user_id}.
type PurgeUserRequest struct {
	UserID string `path:"user_id"          validate:"required,len=24,hexadecimal"`
	Caller string `header:"everfit-source" validate:"omitempty,max=64"`
}

// PurgeUserResponse counts what the purge deleted; both are 0 on a repeat call.
type PurgeUserResponse struct {
	DeletedNotifications int64 `json:"deleted_notifications"`
	DeletedDevices       int64 `json:"deleted_devices"`
}

// FeedResponse is one feed page.
type FeedResponse struct {
	Items      []ItemResponse `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	HasMore    bool           `json:"has_more"`
}

// ItemResponse is one feed card on the wire.
type ItemResponse struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Tab        string           `json:"tab"`
	Title      string           `json:"title"`
	Body       string           `json:"body"`
	Image      ImageResponse    `json:"image"`
	Navigate   NavigateResponse `json:"navigate"`
	Buttons    []string         `json:"buttons"`
	IsRead     bool             `json:"is_read"`
	ActivityAt time.Time        `json:"activity_at"`
	CreatedAt  time.Time        `json:"created_at"`
}

// ImageResponse is the card image on the wire.
type ImageResponse struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	BadgeURL string `json:"badge_url,omitempty"`
}

// NavigateResponse is the first-tap destination on the wire.
type NavigateResponse struct {
	Type        string `json:"type"`
	ChallengeID string `json:"challenge_id,omitempty"`
}

// SummaryResponse is the badge / tab-dot / group counts payload.
type SummaryResponse struct {
	UnreadTotal int64               `json:"unread_total"`
	UnreadByTab UnreadByTabResponse `json:"unread_by_tab"`
	Groups      GroupsResponse      `json:"groups"`
}

// UnreadByTabResponse holds unread counts per tab.
type UnreadByTabResponse struct {
	Activities int64 `json:"activities"`
	System     int64 `json:"system"`
}

// GroupsResponse holds the today / this-week / earlier counts.
type GroupsResponse struct {
	Today    int64 `json:"today"`
	ThisWeek int64 `json:"this_week"`
	Earlier  int64 `json:"earlier"`
}

// ResponseHeaders keeps the personalised feed out of shared caches.
func (FeedResponse) ResponseHeaders() http.Header { return buildPrivateNoStore() }

// ResponseHeaders keeps the personalised summary out of shared caches.
func (SummaryResponse) ResponseHeaders() http.Header { return buildPrivateNoStore() }

// ResponseHeaders keeps the caller's device out of shared caches.
func (DeviceResponse) ResponseHeaders() http.Header { return buildPrivateNoStore() }

// ResponseHeaders keeps the caller's device result out of shared caches.
func (RemoveDeviceResponse) ResponseHeaders() http.Header { return buildPrivateNoStore() }

// List handles GET /notifications.
func (h *Handler) List(ctx context.Context, req ListRequest) (FeedResponse, error) {
	userID, err := parseCallerID(ctx)
	if err != nil {
		return FeedResponse{}, err
	}
	after, err := parseFeedCursor(req.Cursor)
	if err != nil {
		return FeedResponse{}, err
	}
	res, err := h.svc.ListFeed(ctx, ListFeedInput{UserID: userID, Tab: toTab(req.Tab), After: after, Limit: req.Limit})
	if err != nil {
		return FeedResponse{}, mapErr(err)
	}
	return FeedResponse{Items: toItemResponses(res.Items), NextCursor: res.NextCursor, HasMore: res.HasMore}, nil
}

// Summary handles GET /notifications/summary.
func (h *Handler) Summary(ctx context.Context, req SummaryRequest) (SummaryResponse, error) {
	userID, err := parseCallerID(ctx)
	if err != nil {
		return SummaryResponse{}, err
	}
	loc, err := parseTimezone(req.TZ)
	if err != nil {
		return SummaryResponse{}, mapErr(err)
	}
	res, err := h.svc.Summary(ctx, SummaryInput{UserID: userID, Tab: toTab(req.Tab), Location: loc})
	if err != nil {
		return SummaryResponse{}, mapErr(err)
	}
	return toSummaryResponse(res), nil
}

// RegisterDevice handles PUT /devices/{device_id}.
func (h *Handler) RegisterDevice(ctx context.Context, req RegisterDeviceRequest) (DeviceResponse, error) {
	userID, err := parseCallerID(ctx)
	if err != nil {
		return DeviceResponse{}, err
	}
	d, err := h.svc.RegisterDevice(ctx, RegisterDeviceInput{
		UserID:     userID,
		DeviceID:   req.DeviceID,
		Platform:   Platform(req.Platform),
		Token:      req.Token,
		AppVersion: req.AppVersion,
	})
	if err != nil {
		return DeviceResponse{}, mapErr(err)
	}
	return DeviceResponse{DeviceID: d.DeviceID, Platform: string(d.Platform), UpdatedAt: d.UpdatedAt}, nil
}

// RemoveDevice handles DELETE /devices/{device_id}; an unknown device is 200 with removed=false.
func (h *Handler) RemoveDevice(ctx context.Context, req RemoveDeviceRequest) (RemoveDeviceResponse, error) {
	userID, err := parseCallerID(ctx)
	if err != nil {
		return RemoveDeviceResponse{}, err
	}
	removed, err := h.svc.RemoveDevice(ctx, RemoveDeviceInput{UserID: userID, DeviceID: req.DeviceID})
	if err != nil {
		return RemoveDeviceResponse{}, mapErr(err)
	}
	return RemoveDeviceResponse{Removed: removed}, nil
}

// PurgeUser handles DELETE /internal/notifications/users/{user_id} (account deletion).
func (h *Handler) PurgeUser(ctx context.Context, req PurgeUserRequest) (PurgeUserResponse, error) {
	userID, err := bson.ObjectIDFromHex(req.UserID)
	if err != nil || userID.IsZero() {
		return PurgeUserResponse{}, apperr.BadRequest(localization.CodeInvalidRequest, "user_id must be a non-zero ObjectID")
	}
	res, err := h.svc.PurgeUser(ctx, PurgeUserInput{UserID: userID, Caller: req.Caller})
	if err != nil {
		return PurgeUserResponse{}, mapErr(err)
	}
	return PurgeUserResponse{DeletedNotifications: res.Notifications, DeletedDevices: res.Devices}, nil
}

// parseCallerID reads the JWT `sub`; a non-hex or zero ObjectID is treated as unauthenticated.
func parseCallerID(ctx context.Context) (bson.ObjectID, error) {
	id, err := bson.ObjectIDFromHex(middleware.UserIDFromContext(ctx))
	if err != nil {
		return bson.ObjectID{}, buildCallerUnauthorized().Wrap(err)
	}
	if id.IsZero() {
		return bson.ObjectID{}, buildCallerUnauthorized()
	}
	return id, nil
}

func buildCallerUnauthorized() *apperr.AppError {
	return apperr.Unauthorized(localization.CodeUnauthorized, "auth required").
		WithHeader("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
}

func toTab(s string) Tab {
	if s == "" {
		return TabAll
	}
	return Tab(s)
}

func toItemResponses(items []Notification) []ItemResponse {
	out := make([]ItemResponse, 0, len(items))
	for _, it := range items {
		out = append(out, toItemResponse(it))
	}
	return out
}

func toItemResponse(n Notification) ItemResponse {
	buttons := make([]string, 0, len(n.Buttons))
	if n.ButtonsHiddenAt == nil {
		for _, b := range n.Buttons {
			buttons = append(buttons, string(b))
		}
	}
	return ItemResponse{
		ID:    n.ID.Hex(),
		Kind:  string(n.Kind),
		Tab:   string(n.Tab),
		Title: n.Title,
		Body:  n.Body,
		Image: ImageResponse{
			Type:     string(n.Image.Type),
			URL:      n.Image.URL,
			BadgeURL: n.Image.BadgeURL,
		},
		Navigate: NavigateResponse{
			Type:        string(n.Navigate.Type),
			ChallengeID: n.Navigate.ChallengeID,
		},
		Buttons:    buttons,
		IsRead:     n.ReadAt != nil,
		ActivityAt: n.ActivityAt,
		CreatedAt:  n.CreatedAt,
	}
}

func toSummaryResponse(r SummaryResult) SummaryResponse {
	return SummaryResponse{
		UnreadTotal: r.UnreadTotal,
		UnreadByTab: UnreadByTabResponse{Activities: r.UnreadActivities, System: r.UnreadSystem},
		Groups:      GroupsResponse{Today: r.Today, ThisWeek: r.ThisWeek, Earlier: r.Earlier},
	}
}

func buildPrivateNoStore() http.Header {
	return http.Header{"Cache-Control": []string{"private, no-store"}}
}
