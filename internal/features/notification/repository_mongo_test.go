package notification

import (
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestBuildFeedFilter(t *testing.T) {
	user := bson.NewObjectID()
	at := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	id := bson.NewObjectID()
	after := feedCursor{ActivityAt: at, ID: id}
	page2 := bson.A{
		bson.M{"activity_at": bson.M{"$lt": at}},
		bson.M{"activity_at": at, "_id": bson.M{"$lt": id}},
	}

	tests := []struct {
		name  string
		tab   Tab
		after feedCursor
		want  bson.M
	}{
		{"all first page", TabAll, feedCursor{}, bson.M{"user_id": user}},
		{"tab first page", TabSystem, feedCursor{}, bson.M{"user_id": user, "tab": TabSystem}},
		{"all with cursor", TabAll, after, bson.M{"user_id": user, "activity_at": bson.M{"$lte": at}, "$or": page2}},
		{"tab with cursor", TabActivities, after, bson.M{"user_id": user, "tab": TabActivities, "activity_at": bson.M{"$lte": at}, "$or": page2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFeedFilter(user, tt.tab, tt.after)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("filter = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildUnreadFilter(t *testing.T) {
	user := bson.NewObjectID()
	got := buildUnreadFilter(user, TabActivities)
	want := bson.M{"user_id": user, "tab": TabActivities, "read_at": nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filter = %#v, want %#v", got, want)
	}
}

func TestBuildRangeFilter(t *testing.T) {
	user := bson.NewObjectID()
	from := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 28, 17, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		tab      Tab
		from, to time.Time
		want     bson.M
	}{
		{"both bounds", TabSystem, from, to, bson.M{"user_id": user, "tab": TabSystem, "activity_at": bson.M{"$gte": from, "$lt": to}}},
		{"from only", TabAll, from, time.Time{}, bson.M{"user_id": user, "activity_at": bson.M{"$gte": from}}},
		{"to only", TabAll, time.Time{}, to, bson.M{"user_id": user, "activity_at": bson.M{"$lt": to}}},
		{"unbounded", TabAll, time.Time{}, time.Time{}, bson.M{"user_id": user}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRangeFilter(user, tt.tab, tt.from, tt.to)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("filter = %#v, want %#v", got, tt.want)
			}
		})
	}
}
