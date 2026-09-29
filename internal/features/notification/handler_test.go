package notification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Everfit-io/go-service-template/internal/platform/httpx/middleware"
)

type mockNotificationService struct {
	listIn     []ListFeedInput
	listRes    ListFeedResult
	summaryIn  []SummaryInput
	summaryRes SummaryResult
	err        error
}

func (m *mockNotificationService) ListFeed(_ context.Context, in ListFeedInput) (ListFeedResult, error) {
	m.listIn = append(m.listIn, in)
	if m.err != nil {
		return ListFeedResult{}, m.err
	}
	return m.listRes, nil
}

func (m *mockNotificationService) Summary(_ context.Context, in SummaryInput) (SummaryResult, error) {
	m.summaryIn = append(m.summaryIn, in)
	if m.err != nil {
		return SummaryResult{}, m.err
	}
	return m.summaryRes, nil
}

// serve drives the request through Routes() with userID stamped as the JWT sub ("" = none).
func serve(t *testing.T, svc *mockNotificationService, userID, target string) *httptest.ResponseRecorder {
	t.Helper()
	routes := NewHandler(svc).Routes()
	req := httptest.NewRequestWithContext(middleware.WithUserID(context.Background(), userID), http.MethodGet, target, http.NoBody)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
	if got := decodeBody(t, rec)["code"]; got != code {
		t.Errorf("code = %v, want %s", got, code)
	}
}

func TestList_InvalidTab(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/?tab=friends")
	assertError(t, rec, http.StatusBadRequest, "INVALID_REQUEST")
	if len(svc.listIn) != 0 {
		t.Errorf("svc calls = %d, want 0", len(svc.listIn))
	}
}

func TestList_NegativeLimit(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/?limit=-1")
	assertError(t, rec, http.StatusBadRequest, "INVALID_REQUEST")
	if len(svc.listIn) != 0 {
		t.Errorf("svc calls = %d, want 0", len(svc.listIn))
	}
}

func TestList_ZeroLimitPassesThrough(t *testing.T) {
	svc := &mockNotificationService{listRes: ListFeedResult{Items: []Notification{}}}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/?limit=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	if len(svc.listIn) != 1 || svc.listIn[0].Limit != 0 {
		t.Errorf("svc input = %+v, want one call with Limit 0", svc.listIn)
	}
}

func TestList_BadCursor(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/?cursor=not-a-cursor")
	assertError(t, rec, http.StatusBadRequest, "INVALID_REQUEST")
	if len(svc.listIn) != 0 {
		t.Errorf("svc calls = %d, want 0", len(svc.listIn))
	}
}

func TestList_NoUser(t *testing.T) {
	svc := &mockNotificationService{}
	assertError(t, serve(t, svc, "", "/"), http.StatusUnauthorized, "UNAUTHORIZED")
	if len(svc.listIn) != 0 {
		t.Errorf("svc calls = %d, want 0", len(svc.listIn))
	}
}

func TestList_ZeroObjectIDUser(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serve(t, svc, bson.ObjectID{}.Hex(), "/")
	assertError(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("WWW-Authenticate missing on 401")
	}
	if len(svc.listIn) != 0 {
		t.Errorf("svc calls = %d, want 0", len(svc.listIn))
	}
}

func TestList_PassesUserTabAndCursor(t *testing.T) {
	user := bson.NewObjectID()
	after := feedCursor{ActivityAt: time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC), ID: bson.NewObjectID()}
	svc := &mockNotificationService{listRes: ListFeedResult{Items: []Notification{}}}
	rec := serve(t, svc, user.Hex(), "/?tab=system&limit=5&cursor="+after.encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	want := ListFeedInput{UserID: user, Tab: TabSystem, After: after, Limit: 5}
	if len(svc.listIn) != 1 || svc.listIn[0] != want {
		t.Errorf("svc input = %+v, want %+v", svc.listIn, want)
	}
}

func TestList_DefaultTabIsAll(t *testing.T) {
	svc := &mockNotificationService{listRes: ListFeedResult{Items: []Notification{}}}
	serve(t, svc, bson.NewObjectID().Hex(), "/")
	if len(svc.listIn) != 1 || svc.listIn[0].Tab != TabAll {
		t.Errorf("svc input = %+v, want Tab all", svc.listIn)
	}
}

func TestList_MapsItems(t *testing.T) {
	hidden := time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC)
	activity := time.Date(2026, 9, 27, 5, 0, 0, 0, time.UTC)
	created := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)
	read := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	visible := Notification{
		ID: bson.NewObjectID(), Kind: KindChallengePublished, Tab: TabSystem,
		Title: "t", Body: "b", ActivityAt: activity, CreatedAt: created,
		Image:    Image{Type: ImageTypeChallenge, URL: "https://cdn/x.png", BadgeURL: "https://cdn/b.png"},
		Navigate: Navigate{Type: NavigateChallengeDetail, ChallengeID: "abc"},
		Buttons:  []Button{ButtonJumpIn, ButtonNah},
	}
	hiddenRead := Notification{
		ID: bson.NewObjectID(), Kind: KindFriendInvite, Tab: TabActivities,
		Buttons: []Button{ButtonJumpIn}, ButtonsHiddenAt: &hidden, ReadAt: &read,
	}
	svc := &mockNotificationService{listRes: ListFeedResult{Items: []Notification{visible, hiddenRead}, NextCursor: "next", HasMore: true}}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	var got FeedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	v, h := got.Items[0], got.Items[1]
	if v.ID != visible.ID.Hex() || v.Kind != "challenge_published" || v.Tab != "system" || v.IsRead {
		t.Errorf("visible item = %+v", v)
	}
	if v.Title != "t" || v.Body != "b" || !v.ActivityAt.Equal(activity) || !v.CreatedAt.Equal(created) {
		t.Errorf("visible text/times = %q %q %v %v", v.Title, v.Body, v.ActivityAt, v.CreatedAt)
	}
	if strings.Join(v.Buttons, ",") != "jump_in,nah" {
		t.Errorf("visible buttons = %v, want [jump_in nah]", v.Buttons)
	}
	if v.Image != (ImageResponse{Type: "challenge", URL: "https://cdn/x.png", BadgeURL: "https://cdn/b.png"}) {
		t.Errorf("image = %+v", v.Image)
	}
	if v.Navigate != (NavigateResponse{Type: "challenge_detail", ChallengeID: "abc"}) {
		t.Errorf("navigate = %+v", v.Navigate)
	}
	if h.Buttons == nil || len(h.Buttons) != 0 || !h.IsRead {
		t.Errorf("hidden item buttons = %v is_read = %v, want [] true", h.Buttons, h.IsRead)
	}
	if got.NextCursor != "next" || !got.HasMore {
		t.Errorf("page = %q %v, want next true", got.NextCursor, got.HasMore)
	}
}

func TestList_NoAuditFieldsOnWire(t *testing.T) {
	actor := Actor{Type: ActorTypeSystem, ID: "x"}
	svc := &mockNotificationService{listRes: ListFeedResult{Items: []Notification{{
		ID: bson.NewObjectID(), UserID: bson.NewObjectID(),
		DedupeKey: "d", StackKey: "s", Stack: &Stack{Count: 1},
		Source: Source{Type: SourceTypeEvent}, Push: PushState{SentCount: 1},
		CreatedBy: actor, UpdatedBy: &actor,
	}}}}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/")
	items, ok := decodeBody(t, rec)["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want one", decodeBody(t, rec)["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item = %T, want object", items[0])
	}
	for _, k := range []string{"created_by", "updated_by", "source", "push", "stack", "dedupe_key", "stack_key", "user_id"} {
		if _, found := item[k]; found {
			t.Errorf("wire item has %q", k)
		}
	}
}

func TestList_EmptyItemsArray(t *testing.T) {
	svc := &mockNotificationService{listRes: ListFeedResult{Items: []Notification{}}}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/")
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want \"items\":[]", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "next_cursor") {
		t.Errorf("body = %s, want no next_cursor when has_more is false", rec.Body.String())
	}
}

func TestList_ServiceError(t *testing.T) {
	svc := &mockNotificationService{err: errors.New("mongo down")}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/")
	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(rec.Body.String(), "mongo down") {
		t.Errorf("body leaks internal error: %s", rec.Body.String())
	}
}

func TestList_CacheControl(t *testing.T) {
	svc := &mockNotificationService{listRes: ListFeedResult{Items: []Notification{}}}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/")
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}
}

func TestSummary_CacheControl(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/summary")
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}
}

func TestSummary_UnknownTZ(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serve(t, svc, bson.NewObjectID().Hex(), "/summary?tz=Mars/Olympus")
	assertError(t, rec, http.StatusBadRequest, "INVALID_REQUEST")
	if len(svc.summaryIn) != 0 {
		t.Errorf("svc calls = %d, want 0", len(svc.summaryIn))
	}
}

func TestSummary_NoUser(t *testing.T) {
	svc := &mockNotificationService{}
	assertError(t, serve(t, svc, "", "/summary"), http.StatusUnauthorized, "UNAUTHORIZED")
	if len(svc.summaryIn) != 0 {
		t.Errorf("svc calls = %d, want 0", len(svc.summaryIn))
	}
}

func TestSummary_MapsShape(t *testing.T) {
	user := bson.NewObjectID()
	svc := &mockNotificationService{summaryRes: SummaryResult{UnreadTotal: 3, UnreadActivities: 2, UnreadSystem: 1, Today: 4, ThisWeek: 5, Earlier: 6}}
	rec := serve(t, svc, user.Hex(), "/summary?tab=activities&tz=Asia/Saigon")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	want := `{"unread_total":3,"unread_by_tab":{"activities":2,"system":1},"groups":{"today":4,"this_week":5,"earlier":6}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	if len(svc.summaryIn) != 1 {
		t.Fatalf("svc calls = %d, want 1", len(svc.summaryIn))
	}
	in := svc.summaryIn[0]
	if in.UserID != user || in.Tab != TabActivities || in.Location.String() != "Asia/Saigon" {
		t.Errorf("svc input = %+v", in)
	}
}

func TestSummary_DefaultTZIsUTC(t *testing.T) {
	svc := &mockNotificationService{}
	serve(t, svc, bson.NewObjectID().Hex(), "/summary")
	if len(svc.summaryIn) != 1 || svc.summaryIn[0].Location != time.UTC || svc.summaryIn[0].Tab != TabAll {
		t.Errorf("svc input = %+v, want UTC + tab all", svc.summaryIn)
	}
}
