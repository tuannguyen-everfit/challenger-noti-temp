package notification

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
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

// mockRepo returns canned responses and records arguments; it never filters or sorts.
type mockRepo struct {
	mu          sync.Mutex
	feedPages   [][]Notification
	feedCalls   []feedCall
	unread      map[Tab]int64
	unreadCalls []unreadCall
	rangeCounts map[rangeBounds]int64
	rangeCalls  []rangeCall
	err         error
}

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
	repo := &mockRepo{}
	svc := New(repo, Config{MaxListLimit: 100})
	svc.now = frozenTime(now)
	return svc, repo
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
