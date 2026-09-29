package notification

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.opentelemetry.io/otel/trace"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/middleware"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

type feedCall struct {
	userID bson.ObjectID
	tab    Tab
	after  feedCursor
	limit  int
}

type unreadCall struct {
	userID bson.ObjectID
	tab    Tab
}

type rangeCall struct {
	userID   bson.ObjectID
	tab      Tab
	from, to time.Time
}

type rangeBounds struct{ from, to time.Time }

// mockRepo returns canned feed / count responses and records their arguments; devices are an
// in-memory table with the unique indexes enforced. WithTransaction restores devices and the
// linked audit entries when fn fails, so rollback is observable.
type mockRepo struct {
	mu          sync.Mutex
	feedPages   [][]Notification
	feedCalls   []feedCall
	unread      map[Tab]int64
	unreadCalls []unreadCall
	rangeCounts map[rangeBounds]int64
	rangeCalls  []rangeCall
	err         error

	devices       map[bson.ObjectID]Device
	notifications map[bson.ObjectID]Notification // rows the write paths change; the feed reads use feedPages
	failOn        map[string]error               // method name → injected error
	audit         *mockAuditWriter
}

type mockAuditWriter struct {
	mu      sync.Mutex
	entries []AuditLog
	err     error
}

func (m *mockAuditWriter) Write(_ context.Context, e AuditLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, e)
	return nil
}

func (m *mockAuditWriter) snapshot() []AuditLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AuditLog(nil), m.entries...)
}

func (m *mockAuditWriter) restore(entries []AuditLog) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = entries
}

func (m *mockRepo) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	m.mu.Lock()
	devices, notifications := maps.Clone(m.devices), maps.Clone(m.notifications)
	m.mu.Unlock()
	entries := m.audit.snapshot()

	if err := fn(ctx); err != nil {
		m.mu.Lock()
		m.devices, m.notifications = devices, notifications
		m.mu.Unlock()
		m.audit.restore(entries)
		return err
	}
	return nil
}

func (m *mockRepo) FindDevice(_ context.Context, userID bson.ObjectID, deviceID string) (Device, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["FindDevice"]; err != nil {
		return Device{}, false, err
	}
	for _, d := range m.devices {
		if d.UserID == userID && d.DeviceID == deviceID {
			return d, true, nil
		}
	}
	return Device{}, false, nil
}

func (m *mockRepo) FindDeviceByToken(_ context.Context, token string) (Device, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["FindDeviceByToken"]; err != nil {
		return Device{}, false, err
	}
	for _, d := range m.devices {
		if d.Token == token {
			return d, true, nil
		}
	}
	return Device{}, false, nil
}

func (m *mockRepo) UpsertDevice(_ context.Context, d Device) (bson.ObjectID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["UpsertDevice"]; err != nil {
		return bson.ObjectID{}, err
	}
	var current *Device
	for id, row := range m.devices {
		switch {
		case row.UserID == d.UserID && row.DeviceID == d.DeviceID:
			current, d.ID = &row, id
		case row.Token == d.Token:
			return bson.ObjectID{}, errDuplicateToken
		}
	}
	if current == nil {
		d.ID = bson.NewObjectID()
	} else {
		d.CreatedAt, d.CreatedBy = current.CreatedAt, current.CreatedBy
	}
	m.devices[d.ID] = d
	return d.ID, nil
}

func (m *mockRepo) TouchDevice(_ context.Context, id bson.ObjectID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["TouchDevice"]; err != nil {
		return err
	}
	d := m.devices[id]
	d.LastRegisteredAt = at
	m.devices[id] = d
	return nil
}

func (m *mockRepo) DeleteDevice(_ context.Context, id bson.ObjectID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["DeleteDevice"]; err != nil {
		return false, err
	}
	_, ok := m.devices[id]
	delete(m.devices, id)
	return ok, nil
}

func (m *mockRepo) DeleteNotificationsByUser(_ context.Context, userID bson.ObjectID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["DeleteNotificationsByUser"]; err != nil {
		return 0, err
	}
	var n int64
	for id, row := range m.notifications {
		if row.UserID == userID {
			delete(m.notifications, id)
			n++
		}
	}
	return n, nil
}

func (m *mockRepo) DeleteDevicesByUser(_ context.Context, userID bson.ObjectID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["DeleteDevicesByUser"]; err != nil {
		return 0, err
	}
	var n int64
	for id, row := range m.devices {
		if row.UserID == userID {
			delete(m.devices, id)
			n++
		}
	}
	return n, nil
}

// errDuplicateToken is what the unique `devices_token` index would raise.
var errDuplicateToken = errors.New("mock: duplicate token")

func (m *mockRepo) ListFeed(_ context.Context, userID bson.ObjectID, tab Tab, after feedCursor, limit int) ([]Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feedCalls = append(m.feedCalls, feedCall{userID: userID, tab: tab, after: after, limit: limit})
	if m.err != nil {
		return nil, m.err
	}
	if len(m.feedPages) == 0 {
		return nil, nil
	}
	page := m.feedPages[0]
	m.feedPages = m.feedPages[1:]
	return page, nil
}

func (m *mockRepo) CountUnread(_ context.Context, userID bson.ObjectID, tab Tab) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unreadCalls = append(m.unreadCalls, unreadCall{userID: userID, tab: tab})
	if m.err != nil {
		return 0, m.err
	}
	return m.unread[tab], nil
}

func (m *mockRepo) CountRange(_ context.Context, userID bson.ObjectID, tab Tab, from, to time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rangeCalls = append(m.rangeCalls, rangeCall{userID: userID, tab: tab, from: from, to: to})
	if m.err != nil {
		return 0, m.err
	}
	return m.rangeCounts[rangeBounds{from: from.UTC(), to: to.UTC()}], nil
}

func newSvc(now time.Time) (*Service, *mockRepo) {
	repo := &mockRepo{devices: map[bson.ObjectID]Device{}, notifications: map[bson.ObjectID]Notification{}, audit: &mockAuditWriter{}}
	svc := New(repo, repo.audit, Config{MaxListLimit: 100})
	svc.now = frozenTime(now)
	return svc, repo
}

// seedDevice stores d as an existing row and returns it with its _id.
func seedDevice(t *testing.T, repo *mockRepo, d Device) Device {
	t.Helper()
	d.ID = bson.NewObjectID()
	repo.devices[d.ID] = d
	return d
}

func frozenTime(t time.Time) func() time.Time { return func() time.Time { return t } }

// buildNotifications returns n rows, newest first, one minute apart.
func buildNotifications(t *testing.T, n int, start time.Time) []Notification {
	t.Helper()
	out := make([]Notification, 0, n)
	for i := range n {
		out = append(out, Notification{
			ID:         bson.NewObjectID(),
			Tab:        TabActivities,
			ActivityAt: start.Add(-time.Duration(i) * time.Minute).Truncate(time.Millisecond),
		})
	}
	return out
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func assertBadRequest(t *testing.T, err error) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *apperr.AppError", err)
	}
	if ae.HTTPStatus != http.StatusBadRequest || ae.Code != localization.CodeInvalidRequest {
		t.Errorf("err = %d %s, want 400 INVALID_REQUEST", ae.HTTPStatus, ae.Code)
	}
}

func TestListFeed_PagesOf20(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	svc, repo := newSvc(now)
	user := bson.NewObjectID()
	all := buildNotifications(t, 45, now)
	// Repo is asked for limit+1; the extra row only signals has_more.
	repo.feedPages = [][]Notification{all[0:21], all[20:41], all[40:45]}

	var after feedCursor
	wantLens := []int{20, 20, 5}
	wantMore := []bool{true, true, false}
	var cursors []feedCursor
	for i := range wantLens {
		res, err := svc.ListFeed(context.Background(), ListFeedInput{UserID: user, Tab: TabAll, After: after, Limit: 20})
		if err != nil {
			t.Fatalf("page %d: %v", i+1, err)
		}
		if len(res.Items) != wantLens[i] || res.HasMore != wantMore[i] {
			t.Errorf("page %d: len=%d has_more=%v, want %d %v", i+1, len(res.Items), res.HasMore, wantLens[i], wantMore[i])
		}
		cursors = append(cursors, after)
		if !res.HasMore {
			if res.NextCursor != "" {
				t.Errorf("page %d: next_cursor = %q, want empty", i+1, res.NextCursor)
			}
			break
		}
		after, err = parseFeedCursor(res.NextCursor)
		if err != nil {
			t.Fatalf("page %d: parse next_cursor: %v", i+1, err)
		}
	}

	if len(repo.feedCalls) != 3 {
		t.Fatalf("repo calls = %d, want 3", len(repo.feedCalls))
	}
	lastOfPage := []Notification{all[19], all[39]}
	for i, c := range repo.feedCalls {
		if c.userID != user || c.tab != TabAll || c.limit != 21 {
			t.Errorf("call %d = %+v, want user %s tab all limit 21", i+1, c, user.Hex())
		}
		if c.after != cursors[i] {
			t.Errorf("call %d after = %+v, want %+v", i+1, c.after, cursors[i])
		}
		if i > 0 {
			want := feedCursor{ActivityAt: lastOfPage[i-1].ActivityAt, ID: lastOfPage[i-1].ID}
			if c.after != want {
				t.Errorf("call %d after = %+v, want last row of page %d %+v", i+1, c.after, i, want)
			}
		}
	}
}

func TestListFeed_PassesTabAndUser(t *testing.T) {
	svc, repo := newSvc(time.Now())
	user := bson.NewObjectID()
	if _, err := svc.ListFeed(context.Background(), ListFeedInput{UserID: user, Tab: TabSystem, Limit: 5}); err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	if len(repo.feedCalls) != 1 {
		t.Fatalf("repo calls = %d, want 1", len(repo.feedCalls))
	}
	want := feedCall{userID: user, tab: TabSystem, limit: 6}
	if repo.feedCalls[0] != want {
		t.Errorf("call = %+v, want %+v", repo.feedCalls[0], want)
	}
}

func TestListFeed_LimitOverMax(t *testing.T) {
	svc, repo := newSvc(time.Now())
	_, err := svc.ListFeed(context.Background(), ListFeedInput{UserID: bson.NewObjectID(), Tab: TabAll, Limit: 101})
	assertBadRequest(t, err)
	if len(repo.feedCalls) != 0 {
		t.Errorf("repo calls = %d, want 0", len(repo.feedCalls))
	}
}

func TestListFeed_ZeroLimitUsesDefault(t *testing.T) {
	svc, repo := newSvc(time.Now())
	if _, err := svc.ListFeed(context.Background(), ListFeedInput{UserID: bson.NewObjectID(), Tab: TabAll}); err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	if len(repo.feedCalls) != 1 || repo.feedCalls[0].limit != 21 {
		t.Errorf("calls = %+v, want one call with limit 21", repo.feedCalls)
	}
}

func TestListFeed_ZeroLimitUsesDefaultClamped(t *testing.T) {
	svc, repo := newSvc(time.Now())
	svc.cfg.MaxListLimit = 10
	if _, err := svc.ListFeed(context.Background(), ListFeedInput{UserID: bson.NewObjectID(), Tab: TabAll}); err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	if len(repo.feedCalls) != 1 || repo.feedCalls[0].limit != 11 {
		t.Errorf("calls = %+v, want one call with limit 11", repo.feedCalls)
	}
}

func TestListFeed_EmptyItemsNotNil(t *testing.T) {
	svc, _ := newSvc(time.Now())
	res, err := svc.ListFeed(context.Background(), ListFeedInput{UserID: bson.NewObjectID(), Tab: TabAll, Limit: 20})
	if err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	if res.Items == nil || len(res.Items) != 0 || res.HasMore || res.NextCursor != "" {
		t.Errorf("result = %+v, want empty non-nil items, no more", res)
	}
}

func TestListFeed_RepoErrorPropagates(t *testing.T) {
	svc, repo := newSvc(time.Now())
	repo.err = errors.New("mongo down")
	_, err := svc.ListFeed(context.Background(), ListFeedInput{UserID: bson.NewObjectID(), Tab: TabAll, Limit: 20})
	if !errors.Is(err, repo.err) {
		t.Errorf("err = %v, want wrapped %v", err, repo.err)
	}
	if len(repo.feedCalls) != 1 {
		t.Errorf("repo calls = %d, want 1", len(repo.feedCalls))
	}
}

func TestParseFeedCursor(t *testing.T) {
	id := bson.NewObjectID()
	at := time.Date(2026, 9, 29, 3, 0, 0, 123456789, time.UTC)
	valid := feedCursor{ActivityAt: at, ID: id}.encode()

	got, err := parseFeedCursor(valid)
	if err != nil {
		t.Fatalf("parse valid: %v", err)
	}
	want := feedCursor{ActivityAt: at.Truncate(time.Millisecond), ID: id}
	if got != want {
		t.Errorf("round trip = %+v, want %+v (ms precision)", got, want)
	}

	if got, err := parseFeedCursor(""); err != nil || !got.IsZero() {
		t.Errorf(`parse "" = %+v, %v; want zero, nil`, got, err)
	}

	invalid := map[string]string{
		"garbage":    "!!!not-base64",
		"empty json": encodeCursorWire(t, `{}`),
		"non-hex id": encodeCursorWire(t, `{"a":"2026-09-29T03:00:00Z","i":"xyz"}`),
		"zero id":    encodeCursorWire(t, `{"a":"2026-09-29T03:00:00Z","i":"000000000000000000000000"}`),
		"zero time":  encodeCursorWire(t, `{"a":"0001-01-01T00:00:00Z","i":"`+id.Hex()+`"}`),
		"not json":   encodeCursorWire(t, `not json`),
	}
	for name, s := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := parseFeedCursor(s)
			assertBadRequest(t, err)
		})
	}
}

func encodeCursorWire(t *testing.T, raw string) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestSummary_UnreadCounts(t *testing.T) {
	svc, repo := newSvc(time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC))
	repo.unread = map[Tab]int64{TabActivities: 2, TabSystem: 1}
	user := bson.NewObjectID()

	res, err := svc.Summary(context.Background(), SummaryInput{UserID: user, Tab: TabAll, Location: time.UTC})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if res.UnreadTotal != 3 || res.UnreadActivities != 2 || res.UnreadSystem != 1 {
		t.Errorf("unread = %d (%d + %d), want 3 (2 + 1)", res.UnreadTotal, res.UnreadActivities, res.UnreadSystem)
	}
	if len(repo.unreadCalls) != 2 {
		t.Fatalf("unread calls = %d, want 2", len(repo.unreadCalls))
	}
	for _, c := range repo.unreadCalls {
		if c.userID != user {
			t.Errorf("unread call user = %s, want %s", c.userID.Hex(), user.Hex())
		}
	}
}

func TestSummary_SaigonBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC) // 10:00 in Asia/Saigon
	svc, repo := newSvc(now)
	todayStart := time.Date(2026, 9, 28, 17, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC)
	repo.rangeCounts = map[rangeBounds]int64{
		{from: todayStart}:                4,
		{from: weekStart, to: todayStart}: 7,
		{to: weekStart}:                   9,
	}
	user := bson.NewObjectID()

	res, err := svc.Summary(context.Background(), SummaryInput{UserID: user, Tab: TabAll, Location: mustLoadLocation(t, "Asia/Saigon")})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if res.Today != 4 || res.ThisWeek != 7 || res.Earlier != 9 {
		t.Errorf("groups = %d/%d/%d, want 4/7/9", res.Today, res.ThisWeek, res.Earlier)
	}
	assertRanges(t, repo.rangeCalls, user, TabAll, []rangeBounds{
		{from: todayStart},
		{from: weekStart, to: todayStart},
		{to: weekStart},
	})
}

func TestSummary_DSTWeekBoundary(t *testing.T) {
	ny := mustLoadLocation(t, "America/New_York")
	// DST ends 2026-11-01 in New York; six local days back from 11-05 crosses it.
	now := time.Date(2026, 11, 5, 15, 0, 0, 0, ny)
	svc, repo := newSvc(now.UTC())
	user := bson.NewObjectID()

	if _, err := svc.Summary(context.Background(), SummaryInput{UserID: user, Tab: TabAll, Location: ny}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	todayStart := time.Date(2026, 11, 5, 0, 0, 0, 0, ny)
	weekStart := time.Date(2026, 10, 30, 0, 0, 0, 0, ny)
	if got := todayStart.Sub(weekStart); got != 6*24*time.Hour+time.Hour {
		t.Fatalf("fixture: week span = %v, want 145h (crosses DST end)", got)
	}
	assertRanges(t, repo.rangeCalls, user, TabAll, []rangeBounds{
		{from: todayStart},
		{from: weekStart, to: todayStart},
		{to: weekStart},
	})
}

func TestSummary_GroupsUseTab_UnreadDoesNot(t *testing.T) {
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	svc, repo := newSvc(now)
	user := bson.NewObjectID()

	if _, err := svc.Summary(context.Background(), SummaryInput{UserID: user, Tab: TabSystem, Location: time.UTC}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	todayStart := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	assertRanges(t, repo.rangeCalls, user, TabSystem, []rangeBounds{
		{from: todayStart},
		{from: weekStart, to: todayStart},
		{to: weekStart},
	})

	tabs := make([]string, 0, len(repo.unreadCalls))
	for _, c := range repo.unreadCalls {
		tabs = append(tabs, string(c.tab))
	}
	sort.Strings(tabs)
	if len(tabs) != 2 || tabs[0] != string(TabActivities) || tabs[1] != string(TabSystem) {
		t.Errorf("unread tabs = %v, want [activities system]", tabs)
	}
}

func TestSummary_RepoErrorPropagates(t *testing.T) {
	svc, repo := newSvc(time.Now())
	repo.err = errors.New("mongo down")
	_, err := svc.Summary(context.Background(), SummaryInput{UserID: bson.NewObjectID(), Tab: TabAll, Location: time.UTC})
	if !errors.Is(err, repo.err) {
		t.Errorf("err = %v, want wrapped %v", err, repo.err)
	}
	if len(repo.unreadCalls)+len(repo.rangeCalls) == 0 {
		t.Error("repo never called")
	}
}

func TestParseTimezone(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"", "UTC", false},
		{"Asia/Saigon", "Asia/Saigon", false},
		{"Local", "", true},
		{"Mars/Olympus", "", true},
		{"+07:00", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := parseTimezone(tt.name)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidTimezone) {
					t.Errorf("err = %v, want ErrInvalidTimezone", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if loc.String() != tt.want {
				t.Errorf("loc = %s, want %s", loc, tt.want)
			}
		})
	}
}

// assertRanges compares recorded CountRange calls to want, ignoring call order (they run in parallel).
func assertRanges(t *testing.T, calls []rangeCall, user bson.ObjectID, tab Tab, want []rangeBounds) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("range calls = %d, want %d", len(calls), len(want))
	}
	got := make(map[rangeBounds]bool, len(calls))
	for _, c := range calls {
		if c.userID != user || c.tab != tab {
			t.Errorf("range call = %+v, want user %s tab %s", c, user.Hex(), tab)
		}
		got[rangeBounds{from: c.from.UTC(), to: c.to.UTC()}] = true
	}
	for _, w := range want {
		if !got[rangeBounds{from: w.from.UTC(), to: w.to.UTC()}] {
			t.Errorf("missing range [%v, %v); got %+v", w.from, w.to, calls)
		}
	}
}

var deviceNow = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

func userActor(id bson.ObjectID) Actor {
	return Actor{Type: ActorTypeUser, ID: id.Hex(), Via: actorViaAPI}
}

func findDeviceRow(t *testing.T, repo *mockRepo, userID bson.ObjectID, deviceID string) Device {
	t.Helper()
	d, ok, err := repo.FindDevice(context.Background(), userID, deviceID)
	if err != nil || !ok {
		t.Fatalf("device (%s, %s) not stored: ok=%v err=%v", userID.Hex(), deviceID, ok, err)
	}
	return d
}

func TestRegisterDevice_CreatesRowAndAudit(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	traceID := trace.TraceID{1, 2, 3}
	ctx := trace.ContextWithSpanContext(middleware.WithRequestID(context.Background(), "req-1"),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: trace.SpanID{4}}))

	got, err := svc.RegisterDevice(ctx, RegisterDeviceInput{UserID: user, DeviceID: "d-1", Platform: PlatformIOS, Token: "tok-1", AppVersion: "1.0"})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	row := findDeviceRow(t, repo, user, "d-1")
	if got.ID != row.ID || row.Token != "tok-1" || row.Platform != PlatformIOS || row.AppVersion != "1.0" {
		t.Errorf("row = %+v, returned %+v", row, got)
	}
	actor := userActor(user)
	if !row.CreatedAt.Equal(deviceNow) || !row.UpdatedAt.Equal(deviceNow) || !row.LastRegisteredAt.Equal(deviceNow) {
		t.Errorf("timestamps = created %v updated %v registered %v, want %v", row.CreatedAt, row.UpdatedAt, row.LastRegisteredAt, deviceNow)
	}
	if row.CreatedBy != actor || row.UpdatedBy == nil || *row.UpdatedBy != actor {
		t.Errorf("created_by / updated_by = %+v / %+v, want %+v", row.CreatedBy, row.UpdatedBy, actor)
	}

	entries := repo.audit.snapshot()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Entity != AuditEntityDevice || e.EntityID != row.ID.Hex() || e.Action != AuditActionCreate || e.Actor != actor {
		t.Errorf("entry = %+v", e)
	}
	if e.UserID == nil || *e.UserID != user || !e.At.Equal(deviceNow) || len(e.Changes) != 0 {
		t.Errorf("entry user / at / changes = %v / %v / %v", e.UserID, e.At, e.Changes)
	}
	if e.RequestID != "req-1" || e.TraceID != traceID.String() {
		t.Errorf("request_id / trace_id = %q / %q", e.RequestID, e.TraceID)
	}
}

func TestRegisterDevice_NewTokenUpdatesSameRow(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	created := deviceNow.Add(-48 * time.Hour)
	seeded := seedDevice(t, repo, Device{
		UserID: user, DeviceID: "d-1", Platform: PlatformIOS, Token: "old-token", AppVersion: "1.0",
		CreatedAt: created, CreatedBy: userActor(user), UpdatedAt: created, LastRegisteredAt: created,
	})

	if _, err := svc.RegisterDevice(context.Background(), RegisterDeviceInput{UserID: user, DeviceID: "d-1", Platform: PlatformIOS, Token: "new-token", AppVersion: "1.1"}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	if len(repo.devices) != 1 {
		t.Fatalf("rows = %d, want 1 (no duplicate)", len(repo.devices))
	}
	row := findDeviceRow(t, repo, user, "d-1")
	if row.ID != seeded.ID || row.Token != "new-token" || row.AppVersion != "1.1" {
		t.Errorf("row = %+v", row)
	}
	if !row.CreatedAt.Equal(created) || !row.UpdatedAt.Equal(deviceNow) {
		t.Errorf("created_at / updated_at = %v / %v", row.CreatedAt, row.UpdatedAt)
	}

	entries := repo.audit.snapshot()
	if len(entries) != 1 || entries[0].Action != AuditActionUpdate || entries[0].EntityID != seeded.ID.Hex() {
		t.Fatalf("entries = %+v, want one update", entries)
	}
	want := map[string]FieldChange{
		"app_version": {From: "1.0", To: "1.1"},
		"token":       {Changed: true},
	}
	if !reflect.DeepEqual(entries[0].Changes, want) {
		t.Errorf("changes = %+v, want %+v", entries[0].Changes, want)
	}
	assertNoTokenInAudit(t, entries, "old-token", "new-token")
}

func TestRegisterDevice_PlatformChangeRecorded(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	seedDevice(t, repo, Device{UserID: user, DeviceID: "d-1", Platform: PlatformIOS, Token: "tok"})

	if _, err := svc.RegisterDevice(context.Background(), RegisterDeviceInput{UserID: user, DeviceID: "d-1", Platform: PlatformAndroid, Token: "tok"}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	entries := repo.audit.snapshot()
	want := map[string]FieldChange{"platform": {From: PlatformIOS, To: PlatformAndroid}}
	if len(entries) != 1 || !reflect.DeepEqual(entries[0].Changes, want) {
		t.Errorf("entries = %+v, want one update with %+v", entries, want)
	}
}

func TestRegisterDevice_IdenticalPutOnlyTouches(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	earlier := deviceNow.Add(-time.Hour)
	seedDevice(t, repo, Device{
		UserID: user, DeviceID: "d-1", Platform: PlatformAndroid, Token: "tok", AppVersion: "2.0",
		UpdatedAt: earlier, LastRegisteredAt: earlier,
	})

	got, err := svc.RegisterDevice(context.Background(), RegisterDeviceInput{UserID: user, DeviceID: "d-1", Platform: PlatformAndroid, Token: "tok", AppVersion: "2.0"})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	row := findDeviceRow(t, repo, user, "d-1")
	if !row.LastRegisteredAt.Equal(deviceNow) || !row.UpdatedAt.Equal(earlier) {
		t.Errorf("last_registered_at / updated_at = %v / %v, want %v / %v", row.LastRegisteredAt, row.UpdatedAt, deviceNow, earlier)
	}
	if !got.UpdatedAt.Equal(earlier) || !got.LastRegisteredAt.Equal(deviceNow) {
		t.Errorf("returned updated_at / last_registered_at = %v / %v", got.UpdatedAt, got.LastRegisteredAt)
	}
	if n := len(repo.audit.snapshot()); n != 0 {
		t.Errorf("audit entries = %d, want 0 for a refresh-only PUT", n)
	}
}

func TestRegisterDevice_TokenHeldByAnotherAccountIsEvicted(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	caller, previous := bson.NewObjectID(), bson.NewObjectID()
	old := seedDevice(t, repo, Device{UserID: previous, DeviceID: "shared-phone", Platform: PlatformIOS, Token: "tok"})

	if _, err := svc.RegisterDevice(context.Background(), RegisterDeviceInput{UserID: caller, DeviceID: "shared-phone", Platform: PlatformIOS, Token: "tok"}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	if _, ok := repo.devices[old.ID]; ok {
		t.Error("previous account's row still holds the token")
	}
	row := findDeviceRow(t, repo, caller, "shared-phone")

	entries := repo.audit.snapshot()
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d, want 2 (delete + create)", len(entries))
	}
	del, create := entries[0], entries[1]
	if del.Action != AuditActionDelete || del.EntityID != old.ID.Hex() || del.UserID == nil || *del.UserID != previous || del.Actor != userActor(caller) {
		t.Errorf("eviction entry = %+v", del)
	}
	if create.Action != AuditActionCreate || create.EntityID != row.ID.Hex() {
		t.Errorf("create entry = %+v", create)
	}
	assertNoTokenInAudit(t, entries, "tok")
}

func TestRegisterDevice_TokenMovedFromCallersOtherDevice(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	other := seedDevice(t, repo, Device{UserID: user, DeviceID: "old-install", Platform: PlatformIOS, Token: "tok"})

	if _, err := svc.RegisterDevice(context.Background(), RegisterDeviceInput{UserID: user, DeviceID: "new-install", Platform: PlatformIOS, Token: "tok"}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	if _, ok := repo.devices[other.ID]; ok || len(repo.devices) != 1 {
		t.Errorf("rows = %+v, want only new-install", repo.devices)
	}
}

func TestRegisterDevice_AuditFailureRollsBack(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	caller, previous := bson.NewObjectID(), bson.NewObjectID()
	old := seedDevice(t, repo, Device{UserID: previous, DeviceID: "phone", Platform: PlatformIOS, Token: "tok"})
	repo.audit.err = errors.New("audit down")

	_, err := svc.RegisterDevice(context.Background(), RegisterDeviceInput{UserID: caller, DeviceID: "phone", Platform: PlatformIOS, Token: "tok"})
	if !errors.Is(err, repo.audit.err) {
		t.Fatalf("err = %v, want the audit error", err)
	}
	if _, ok := repo.devices[old.ID]; !ok || len(repo.devices) != 1 {
		t.Errorf("rows = %+v, want the untouched original only", repo.devices)
	}
}

func TestRegisterDevice_RepoErrorPropagates(t *testing.T) {
	for _, method := range []string{"FindDevice", "FindDeviceByToken", "DeleteDevice", "UpsertDevice", "TouchDevice"} {
		t.Run(method, func(t *testing.T) {
			svc, repo := newSvc(deviceNow)
			user := bson.NewObjectID()
			seedDevice(t, repo, Device{UserID: bson.NewObjectID(), DeviceID: "other", Token: "held"})
			seedDevice(t, repo, Device{UserID: user, DeviceID: "same", Platform: PlatformIOS, Token: "same-tok"})
			boom := errors.New("mongo down")
			repo.failOn = map[string]error{method: boom}

			in := RegisterDeviceInput{UserID: user, DeviceID: "new", Platform: PlatformIOS, Token: "held"}
			if method == "TouchDevice" {
				in = RegisterDeviceInput{UserID: user, DeviceID: "same", Platform: PlatformIOS, Token: "same-tok"}
			}
			if _, err := svc.RegisterDevice(context.Background(), in); !errors.Is(err, boom) {
				t.Errorf("err = %v, want %v", err, boom)
			}
			if n := len(repo.audit.snapshot()); n != 0 {
				t.Errorf("audit entries = %d, want 0", n)
			}
		})
	}
}

func TestRemoveDevice_DeletesAndAudits(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	row := seedDevice(t, repo, Device{UserID: user, DeviceID: "d-1", Platform: PlatformIOS, Token: "tok"})

	removed, err := svc.RemoveDevice(context.Background(), RemoveDeviceInput{UserID: user, DeviceID: "d-1"})
	if err != nil || !removed {
		t.Fatalf("RemoveDevice = %v, %v; want true, nil", removed, err)
	}
	if len(repo.devices) != 0 {
		t.Errorf("rows = %d, want 0", len(repo.devices))
	}
	entries := repo.audit.snapshot()
	if len(entries) != 1 || entries[0].Action != AuditActionDelete || entries[0].EntityID != row.ID.Hex() || entries[0].Actor != userActor(user) {
		t.Errorf("entries = %+v, want one delete", entries)
	}
	assertNoTokenInAudit(t, entries, "tok")
}

func TestRemoveDevice_UnknownDeviceIsNoOp(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	owner := bson.NewObjectID()
	seedDevice(t, repo, Device{UserID: owner, DeviceID: "d-1", Token: "tok"})

	removed, err := svc.RemoveDevice(context.Background(), RemoveDeviceInput{UserID: bson.NewObjectID(), DeviceID: "d-1"})
	if err != nil || removed {
		t.Fatalf("RemoveDevice = %v, %v; want false, nil", removed, err)
	}
	if len(repo.devices) != 1 {
		t.Error("another user's device was removed")
	}
	if n := len(repo.audit.snapshot()); n != 0 {
		t.Errorf("audit entries = %d, want 0", n)
	}
}

func TestRemoveDevice_AuditFailureRollsBack(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	seedDevice(t, repo, Device{UserID: user, DeviceID: "d-1", Token: "tok"})
	repo.audit.err = errors.New("audit down")

	if _, err := svc.RemoveDevice(context.Background(), RemoveDeviceInput{UserID: user, DeviceID: "d-1"}); !errors.Is(err, repo.audit.err) {
		t.Fatalf("err = %v, want the audit error", err)
	}
	if len(repo.devices) != 1 {
		t.Error("device deleted although its audit entry failed")
	}
}

func TestRemoveDevice_RepoErrorPropagates(t *testing.T) {
	for _, method := range []string{"FindDevice", "DeleteDevice"} {
		t.Run(method, func(t *testing.T) {
			svc, repo := newSvc(deviceNow)
			user := bson.NewObjectID()
			seedDevice(t, repo, Device{UserID: user, DeviceID: "d-1", Token: "tok"})
			boom := errors.New("mongo down")
			repo.failOn = map[string]error{method: boom}

			if _, err := svc.RemoveDevice(context.Background(), RemoveDeviceInput{UserID: user, DeviceID: "d-1"}); !errors.Is(err, boom) {
				t.Errorf("err = %v, want %v", err, boom)
			}
		})
	}
}

func TestPickAuditChanges(t *testing.T) {
	got := pickAuditChanges(AuditEntityDevice, map[string]FieldChange{
		"platform":    {From: PlatformIOS, To: PlatformAndroid},
		"token":       {From: "secret-a", To: "secret-b"},
		"device_name": {From: "Pixel 8", To: "x"},
	})
	want := map[string]FieldChange{
		"platform": {From: PlatformIOS, To: PlatformAndroid},
		"token":    {Changed: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pickAuditChanges = %+v, want %+v", got, want)
	}
	if got := pickAuditChanges("unknown", map[string]FieldChange{"platform": {To: "x"}}); len(got) != 0 {
		t.Errorf("unknown entity kept %+v", got)
	}
}

func TestRecordAudit_SkipsNoOpUpdate(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	err := svc.recordAudit(context.Background(), AuditLog{
		Entity: AuditEntityDevice, Action: AuditActionUpdate,
		Changes: map[string]FieldChange{"not_allowed": {To: 1}},
	})
	if err != nil {
		t.Fatalf("recordAudit: %v", err)
	}
	if n := len(repo.audit.snapshot()); n != 0 {
		t.Errorf("audit entries = %d, want 0 for an update with no allow-listed change", n)
	}
}

func TestRunAudited_DuplicateAuditKeyIsSuccess(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	repo.audit.err = errAuditRecorded
	err := svc.runAudited(context.Background(), func(ctx context.Context) error {
		return svc.recordAudit(ctx, AuditLog{Entity: AuditEntityDevice, Action: AuditActionCreate, AuditKey: "event:1"})
	})
	if err != nil {
		t.Errorf("runAudited = %v, want nil (already recorded)", err)
	}
}

// assertNoTokenInAudit fails if a token value reaches any stored entry.
func assertNoTokenInAudit(t *testing.T, entries []AuditLog, tokens ...string) {
	t.Helper()
	for _, e := range entries {
		raw, err := bson.Marshal(e)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		for _, tok := range tokens {
			if bytes.Contains(raw, []byte(tok)) {
				t.Errorf("token %q leaked into audit entry %+v", tok, e)
			}
		}
	}
}

const purgeCaller = "account-deletion"

// seedNotifications stores n rows owned by userID.
func seedNotifications(t *testing.T, repo *mockRepo, userID bson.ObjectID, n int) {
	t.Helper()
	for range n {
		id := bson.NewObjectID()
		repo.notifications[id] = Notification{ID: id, UserID: userID, Tab: TabSystem}
	}
}

func countOwned(repo *mockRepo, userID bson.ObjectID) (notifications, devices int) {
	for _, n := range repo.notifications {
		if n.UserID == userID {
			notifications++
		}
	}
	for _, d := range repo.devices {
		if d.UserID == userID {
			devices++
		}
	}
	return notifications, devices
}

func TestPurgeUser_DeletesBothAndAudits(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user, other := bson.NewObjectID(), bson.NewObjectID()
	seedNotifications(t, repo, user, 12)
	seedDevice(t, repo, Device{UserID: user, DeviceID: "phone", Token: "t1"})
	seedDevice(t, repo, Device{UserID: user, DeviceID: "tablet", Token: "t2"})
	seedNotifications(t, repo, other, 3)
	seedDevice(t, repo, Device{UserID: other, DeviceID: "phone", Token: "t3"})
	earlier := AuditLog{Entity: AuditEntityDevice, Action: AuditActionCreate, UserID: &user}
	repo.audit.entries = []AuditLog{earlier}

	got, err := svc.PurgeUser(context.Background(), PurgeUserInput{UserID: user, Caller: purgeCaller})
	if err != nil {
		t.Fatalf("PurgeUser: %v", err)
	}
	if got != (PurgeUserResult{Notifications: 12, Devices: 2}) {
		t.Errorf("result = %+v, want {12 2}", got)
	}
	if n, d := countOwned(repo, user); n != 0 || d != 0 {
		t.Errorf("user still owns %d notifications, %d devices", n, d)
	}
	if n, d := countOwned(repo, other); n != 3 || d != 1 {
		t.Errorf("other user owns %d notifications, %d devices; want 3, 1", n, d)
	}

	entries := repo.audit.snapshot()
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d, want the earlier one kept + one purge", len(entries))
	}
	e := entries[1]
	wantActor := Actor{Type: ActorTypeService, ID: purgeCaller, Via: actorViaInternalAPI}
	if e.Entity != AuditEntityUser || e.EntityID != user.Hex() || e.Action != AuditActionPurge || e.Actor != wantActor {
		t.Errorf("purge entry = %+v", e)
	}
	if e.UserID == nil || *e.UserID != user || e.Count != 14 || !e.At.Equal(deviceNow) || len(e.Changes) != 0 {
		t.Errorf("purge entry user / count / at / changes = %v / %d / %v / %v", e.UserID, e.Count, e.At, e.Changes)
	}
}

func TestPurgeUser_SecondCallIsNoOp(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	seedNotifications(t, repo, user, 2)
	in := PurgeUserInput{UserID: user, Caller: purgeCaller}
	if _, err := svc.PurgeUser(context.Background(), in); err != nil {
		t.Fatalf("first PurgeUser: %v", err)
	}

	got, err := svc.PurgeUser(context.Background(), in)
	if err != nil || got != (PurgeUserResult{}) {
		t.Fatalf("second PurgeUser = %+v, %v; want zero counts, nil", got, err)
	}
	if n := len(repo.audit.snapshot()); n != 1 {
		t.Errorf("audit entries = %d, want 1 (the second purge deleted nothing)", n)
	}
}

func TestPurgeUser_AuditFailureDeletesNothing(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	user := bson.NewObjectID()
	seedNotifications(t, repo, user, 4)
	seedDevice(t, repo, Device{UserID: user, DeviceID: "phone", Token: "t1"})
	repo.audit.err = errors.New("audit down")

	if _, err := svc.PurgeUser(context.Background(), PurgeUserInput{UserID: user, Caller: purgeCaller}); !errors.Is(err, repo.audit.err) {
		t.Fatalf("err = %v, want the audit error", err)
	}
	if n, d := countOwned(repo, user); n != 4 || d != 1 {
		t.Errorf("after a failed purge the user owns %d notifications, %d devices; want 4, 1", n, d)
	}
}

func TestPurgeUser_RepoErrorPropagates(t *testing.T) {
	for _, method := range []string{"DeleteNotificationsByUser", "DeleteDevicesByUser"} {
		t.Run(method, func(t *testing.T) {
			svc, repo := newSvc(deviceNow)
			user := bson.NewObjectID()
			seedNotifications(t, repo, user, 1)
			seedDevice(t, repo, Device{UserID: user, DeviceID: "phone", Token: "t1"})
			boom := errors.New("mongo down")
			repo.failOn = map[string]error{method: boom}

			if _, err := svc.PurgeUser(context.Background(), PurgeUserInput{UserID: user}); !errors.Is(err, boom) {
				t.Errorf("err = %v, want %v", err, boom)
			}
			if n, d := countOwned(repo, user); n != 1 || d != 1 {
				t.Errorf("rows after failure = %d, %d; want 1, 1", n, d)
			}
		})
	}
}

func TestRecordAudit_SkipsEmptyPurge(t *testing.T) {
	svc, repo := newSvc(deviceNow)
	if err := svc.recordAudit(context.Background(), AuditLog{Entity: AuditEntityUser, Action: AuditActionPurge}); err != nil {
		t.Fatalf("recordAudit: %v", err)
	}
	if n := len(repo.audit.snapshot()); n != 0 {
		t.Errorf("audit entries = %d, want 0 for a purge that deleted nothing", n)
	}
}
