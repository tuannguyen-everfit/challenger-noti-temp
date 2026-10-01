package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Everfit-io/go-service-template/internal/platform/httpx/middleware"
)

type mockNotificationService struct {
	listIn      []ListFeedInput
	listRes     ListFeedResult
	summaryIn   []SummaryInput
	summaryRes  SummaryResult
	registerIn  []RegisterDeviceInput
	registerRes Device
	removeIn    []RemoveDeviceInput
	removeRes   bool
	purgeIn     []PurgeUserInput
	purgeRes    PurgeUserResult
	readIn      []MarkReadInput
	readRes     MarkReadResult
	readAllIn   []MarkAllReadInput
	readAllRes  int64
	err         error
}

func (m *mockNotificationService) MarkRead(_ context.Context, in MarkReadInput) (MarkReadResult, error) {
	m.readIn = append(m.readIn, in)
	if m.err != nil {
		return MarkReadResult{}, m.err
	}
	return m.readRes, nil
}

func (m *mockNotificationService) MarkAllRead(_ context.Context, in MarkAllReadInput) (int64, error) {
	m.readAllIn = append(m.readAllIn, in)
	if m.err != nil {
		return 0, m.err
	}
	return m.readAllRes, nil
}

func (m *mockNotificationService) PurgeUser(_ context.Context, in PurgeUserInput) (PurgeUserResult, error) {
	m.purgeIn = append(m.purgeIn, in)
	if m.err != nil {
		return PurgeUserResult{}, m.err
	}
	return m.purgeRes, nil
}

func (m *mockNotificationService) RegisterDevice(_ context.Context, in RegisterDeviceInput) (Device, error) {
	m.registerIn = append(m.registerIn, in)
	if m.err != nil {
		return Device{}, m.err
	}
	return m.registerRes, nil
}

func (m *mockNotificationService) RemoveDevice(_ context.Context, in RemoveDeviceInput) (bool, error) {
	m.removeIn = append(m.removeIn, in)
	if m.err != nil {
		return false, m.err
	}
	return m.removeRes, nil
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

// serveDevices drives a request through DeviceRoutes() with userID stamped as the JWT sub ("" = none).
func serveDevices(t *testing.T, svc *mockNotificationService, userID, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(middleware.WithUserID(context.Background(), userID), method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewHandler(svc).DeviceRoutes().ServeHTTP(rec, req)
	return rec
}

func TestRegisterDevice_PassesInputAndHidesToken(t *testing.T) {
	user := bson.NewObjectID()
	updated := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	svc := &mockNotificationService{registerRes: Device{DeviceID: "d-1", Platform: PlatformIOS, Token: "secret-token", UpdatedAt: updated}}

	rec := serveDevices(t, svc, user.Hex(), http.MethodPut, "/d-1", `{"platform":"ios","token":"secret-token","app_version":"1.4.0"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	want := RegisterDeviceInput{UserID: user, DeviceID: "d-1", Platform: PlatformIOS, Token: "secret-token", AppVersion: "1.4.0"}
	if len(svc.registerIn) != 1 || svc.registerIn[0] != want {
		t.Fatalf("svc input = %+v, want %+v", svc.registerIn, want)
	}
	body := decodeBody(t, rec)
	if body["device_id"] != "d-1" || body["platform"] != "ios" || body["updated_at"] != "2026-09-29T10:00:00Z" {
		t.Errorf("body = %v", body)
	}
	if strings.Contains(rec.Body.String(), "secret-token") {
		t.Error("token echoed on the wire")
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestRegisterDevice_AppVersionOptional(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serveDevices(t, svc, bson.NewObjectID().Hex(), http.MethodPut, "/d-1", `{"platform":"android","token":"t"}`)
	if rec.Code != http.StatusOK || len(svc.registerIn) != 1 || svc.registerIn[0].AppVersion != "" {
		t.Errorf("status = %d, input = %+v", rec.Code, svc.registerIn)
	}
}

func TestRegisterDevice_InvalidRequest(t *testing.T) {
	longID := strings.Repeat("d", 129)
	cases := map[string]struct{ target, body string }{
		"web platform":       {"/d-1", `{"platform":"web","token":"t"}`},
		"missing platform":   {"/d-1", `{"token":"t"}`},
		"empty token":        {"/d-1", `{"platform":"ios","token":""}`},
		"missing token":      {"/d-1", `{"platform":"ios"}`},
		"device_id too long": {"/" + longID, `{"platform":"ios","token":"t"}`},
		"unknown field":      {"/d-1", `{"platform":"ios","token":"t","user_id":"x"}`},
		"no body":            {"/d-1", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &mockNotificationService{}
			rec := serveDevices(t, svc, bson.NewObjectID().Hex(), http.MethodPut, tc.target, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			if len(svc.registerIn) != 0 {
				t.Errorf("svc called %d times, want 0", len(svc.registerIn))
			}
		})
	}
}

func TestRegisterDevice_NoUser(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serveDevices(t, svc, "", http.MethodPut, "/d-1", `{"platform":"ios","token":"t"}`)
	assertError(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
	if len(svc.registerIn) != 0 {
		t.Error("svc called without a caller")
	}
}

func TestRegisterDevice_ServiceError(t *testing.T) {
	svc := &mockNotificationService{err: errors.New("audit write: mongo down")}
	rec := serveDevices(t, svc, bson.NewObjectID().Hex(), http.MethodPut, "/d-1", `{"platform":"ios","token":"t"}`)
	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(rec.Body.String(), "mongo") {
		t.Errorf("internal error leaked: %s", rec.Body.String())
	}
}

func TestRemoveDevice_ReportsRemoved(t *testing.T) {
	for _, removed := range []bool{true, false} {
		user := bson.NewObjectID()
		svc := &mockNotificationService{removeRes: removed}
		rec := serveDevices(t, svc, user.Hex(), http.MethodDelete, "/d-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
		if got := decodeBody(t, rec)["removed"]; got != removed {
			t.Errorf("removed = %v, want %v", got, removed)
		}
		if want := (RemoveDeviceInput{UserID: user, DeviceID: "d-1"}); len(svc.removeIn) != 1 || svc.removeIn[0] != want {
			t.Errorf("svc input = %+v, want %+v", svc.removeIn, want)
		}
	}
}

func TestRemoveDevice_InvalidRequest(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serveDevices(t, svc, bson.NewObjectID().Hex(), http.MethodDelete, "/"+strings.Repeat("d", 129), "")
	assertError(t, rec, http.StatusBadRequest, "INVALID_REQUEST")
	if len(svc.removeIn) != 0 {
		t.Error("svc called on an invalid request")
	}
}

func TestRemoveDevice_NoUser(t *testing.T) {
	rec := serveDevices(t, &mockNotificationService{}, "", http.MethodDelete, "/d-1", "")
	assertError(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
}

func TestRemoveDevice_ServiceError(t *testing.T) {
	svc := &mockNotificationService{err: errors.New("mongo down")}
	rec := serveDevices(t, svc, bson.NewObjectID().Hex(), http.MethodDelete, "/d-1", "")
	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
}

// serveInternal drives a request through RegisterInternalRoutes (the secret check is the router's job).
func serveInternal(t *testing.T, svc *mockNotificationService, target string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	NewHandler(svc).RegisterInternalRoutes(r)
	req := httptest.NewRequest(http.MethodDelete, target, http.NoBody)
	maps.Copy(req.Header, header)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPurgeUser_PassesUserAndCaller(t *testing.T) {
	user := bson.NewObjectID()
	svc := &mockNotificationService{purgeRes: PurgeUserResult{Notifications: 12, Devices: 2}}

	rec := serveInternal(t, svc, "/internal/notifications/users/"+user.Hex(), http.Header{"Everfit-Source": {"account-deletion"}})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if want := (PurgeUserInput{UserID: user, Caller: "account-deletion"}); len(svc.purgeIn) != 1 || svc.purgeIn[0] != want {
		t.Fatalf("svc input = %+v, want %+v", svc.purgeIn, want)
	}
	body := decodeBody(t, rec)
	if body["deleted_notifications"] != float64(12) || body["deleted_devices"] != float64(2) {
		t.Errorf("body = %v", body)
	}
}

func TestPurgeUser_CallerOptional(t *testing.T) {
	svc := &mockNotificationService{}
	rec := serveInternal(t, svc, "/internal/notifications/users/"+bson.NewObjectID().Hex(), nil)
	if rec.Code != http.StatusOK || len(svc.purgeIn) != 1 || svc.purgeIn[0].Caller != "" {
		t.Errorf("status = %d, input = %+v", rec.Code, svc.purgeIn)
	}
	if body := decodeBody(t, rec); body["deleted_notifications"] != float64(0) || body["deleted_devices"] != float64(0) {
		t.Errorf("zero counts must still be on the wire: %v", body)
	}
}

func TestPurgeUser_InvalidRequest(t *testing.T) {
	cases := map[string]struct {
		userID string
		header http.Header
	}{
		"not hex":         {userID: "not-an-object-id-at-all!"},
		"short":           {userID: "abc"},
		"zero ObjectID":   {userID: bson.ObjectID{}.Hex()},
		"caller too long": {userID: bson.NewObjectID().Hex(), header: http.Header{"Everfit-Source": {strings.Repeat("s", 65)}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &mockNotificationService{}
			rec := serveInternal(t, svc, "/internal/notifications/users/"+tc.userID, tc.header)
			assertError(t, rec, http.StatusBadRequest, "INVALID_REQUEST")
			if len(svc.purgeIn) != 0 {
				t.Error("svc called on an invalid request")
			}
		})
	}
}

func TestPurgeUser_ServiceError(t *testing.T) {
	svc := &mockNotificationService{err: errors.New("audit write: mongo down")}
	rec := serveInternal(t, svc, "/internal/notifications/users/"+bson.NewObjectID().Hex(), nil)
	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(rec.Body.String(), "mongo") {
		t.Errorf("internal error leaked: %s", rec.Body.String())
	}
}

// servePost drives a POST through Routes() with userID stamped as the JWT sub ("" = none).
func servePost(t *testing.T, svc *mockNotificationService, userID, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(middleware.WithUserID(context.Background(), userID), http.MethodPost, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewHandler(svc).Routes().ServeHTTP(rec, req)
	return rec
}

func TestMarkRead_MapsResponse(t *testing.T) {
	user, id := bson.NewObjectID(), bson.NewObjectID()
	now := time.Now()
	svc := &mockNotificationService{readRes: MarkReadResult{
		Notification: Notification{ID: id, ReadAt: &now, ButtonsHiddenAt: &now, Buttons: []Button{ButtonJumpIn, ButtonNah}},
		Navigate:     Navigate{Type: NavigateChallengeDetail, ChallengeID: "c1"},
		Available:    true,
	}}

	rec := servePost(t, svc, user.Hex(), "/"+id.Hex()+"/read", `{"action":"jump_in"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if want := (MarkReadInput{UserID: user, ID: id, Action: ReadActionJumpIn}); len(svc.readIn) != 1 || svc.readIn[0] != want {
		t.Fatalf("svc input = %+v, want %+v", svc.readIn, want)
	}
	body := decodeBody(t, rec)
	if body["id"] != id.Hex() || body["is_read"] != true || body["available"] != true {
		t.Errorf("body = %v", body)
	}
	if buttons, ok := body["buttons"].([]any); !ok || len(buttons) != 0 {
		t.Errorf("buttons = %v, want []", body["buttons"])
	}
	if nav, _ := body["navigate"].(map[string]any); nav["type"] != "challenge_detail" || nav["challenge_id"] != "c1" {
		t.Errorf("navigate = %v", body["navigate"])
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestMarkRead_UnavailableNavigate(t *testing.T) {
	id := bson.NewObjectID()
	now := time.Now()
	svc := &mockNotificationService{readRes: MarkReadResult{
		Notification: Notification{ID: id, ReadAt: &now},
		Navigate:     Navigate{Type: NavigateNone},
	}}
	body := decodeBody(t, servePost(t, svc, bson.NewObjectID().Hex(), "/"+id.Hex()+"/read", `{"action":"tap"}`))
	nav, _ := body["navigate"].(map[string]any)
	if body["available"] != false || nav["type"] != "none" {
		t.Errorf("body = %v", body)
	}
	if _, ok := nav["challenge_id"]; ok {
		t.Errorf("challenge_id on a none navigate: %v", nav)
	}
}

func TestMarkRead_InvalidRequest(t *testing.T) {
	id := bson.NewObjectID().Hex()
	cases := map[string]struct{ target, body string }{
		"read_all is not a tap action": {"/" + id + "/read", `{"action":"read_all"}`},
		"unknown action":               {"/" + id + "/read", `{"action":"swipe"}`},
		"missing action":               {"/" + id + "/read", `{}`},
		"no body":                      {"/" + id + "/read", ""},
		"malformed id":                 {"/not-an-id/read", `{"action":"tap"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &mockNotificationService{}
			rec := servePost(t, svc, bson.NewObjectID().Hex(), tc.target, tc.body)
			assertError(t, rec, http.StatusBadRequest, "INVALID_REQUEST")
			if len(svc.readIn) != 0 {
				t.Error("svc called on an invalid request")
			}
		})
	}
}

func TestMarkRead_NotFound(t *testing.T) {
	svc := &mockNotificationService{err: fmt.Errorf("mark read: %w", ErrNotFound)}
	rec := servePost(t, svc, bson.NewObjectID().Hex(), "/"+bson.NewObjectID().Hex()+"/read", `{"action":"tap"}`)
	assertError(t, rec, http.StatusNotFound, "NOTIFICATION_NOT_FOUND")
}

func TestMarkRead_ChallengerUnavailableIs503(t *testing.T) {
	svc := &mockNotificationService{err: fmt.Errorf("mark read: %w", ErrChallengerUnavailable)}
	rec := servePost(t, svc, bson.NewObjectID().Hex(), "/"+bson.NewObjectID().Hex()+"/read", `{"action":"tap"}`)
	assertError(t, rec, http.StatusServiceUnavailable, "NOTIFICATION_CHALLENGER_UNAVAILABLE")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 without Retry-After")
	}
}

func TestMarkRead_NoUser(t *testing.T) {
	svc := &mockNotificationService{}
	rec := servePost(t, svc, "", "/"+bson.NewObjectID().Hex()+"/read", `{"action":"tap"}`)
	assertError(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
	if len(svc.readIn) != 0 {
		t.Error("svc called without a caller")
	}
}

func TestMarkAllRead_ReportsUpdated(t *testing.T) {
	user := bson.NewObjectID()
	svc := &mockNotificationService{readAllRes: 7}
	for _, body := range []string{"", "{}"} {
		rec := servePost(t, svc, user.Hex(), "/read-all", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("body %q: status = %d, %s", body, rec.Code, rec.Body.String())
		}
		if got := decodeBody(t, rec)["updated"]; got != float64(7) {
			t.Errorf("updated = %v, want 7", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("Cache-Control = %q", got)
		}
	}
	if len(svc.readAllIn) != 2 || svc.readAllIn[0] != (MarkAllReadInput{UserID: user}) {
		t.Errorf("svc input = %+v", svc.readAllIn)
	}
}

func TestMarkAllRead_NoUserAndServiceError(t *testing.T) {
	assertError(t, servePost(t, &mockNotificationService{}, "", "/read-all", ""), http.StatusUnauthorized, "UNAUTHORIZED")
	rec := servePost(t, &mockNotificationService{err: errors.New("mongo down")}, bson.NewObjectID().Hex(), "/read-all", "")
	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
}
