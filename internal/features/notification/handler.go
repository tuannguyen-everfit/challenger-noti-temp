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
}

// Handler serves the notification HTTP endpoints.
type Handler struct{ svc notificationService }

// NewHandler builds the Handler.
func NewHandler(svc notificationService) *Handler { return &Handler{svc: svc} }

// Routes returns the feature sub-router; every route requires Bearer auth.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", typed.JSON(http.StatusOK, h.List))
	r.Get("/summary", typed.JSON(http.StatusOK, h.Summary))
	return r
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
