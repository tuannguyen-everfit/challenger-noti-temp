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

func TestBuildDeviceUpsert(t *testing.T) {
	user := bson.NewObjectID()
	actor := Actor{Type: ActorTypeUser, ID: user.Hex(), Via: actorViaAPI}
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	d := Device{
		UserID: user, DeviceID: "d-1", Platform: PlatformIOS, Token: "tok", AppVersion: "1.0",
		LastRegisteredAt: at, CreatedBy: actor, CreatedAt: at, UpdatedBy: &actor, UpdatedAt: at,
	}

	got := buildDeviceUpsert(d)
	want := bson.M{
		"$set": bson.M{
			"platform": PlatformIOS, "token": "tok", "app_version": "1.0",
			"last_registered_at": at, "updated_at": at, "updated_by": &actor,
		},
		"$setOnInsert": bson.M{"created_at": at, "created_by": actor},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDeviceUpsert = %v, want %v", got, want)
	}

	d.AppVersion = ""
	got = buildDeviceUpsert(d)
	if _, ok := got["$set"].(bson.M)["app_version"]; ok {
		t.Error("empty app_version must not be $set")
	}
	if !reflect.DeepEqual(got["$unset"], bson.M{"app_version": ""}) {
		t.Errorf("$unset = %v, want app_version", got["$unset"])
	}
}

func TestBuildDeviceKeyFilter(t *testing.T) {
	user := bson.NewObjectID()
	if got, want := buildDeviceKeyFilter(user, "d-1"), (bson.M{"user_id": user, "device_id": "d-1"}); !reflect.DeepEqual(got, want) {
		t.Errorf("buildDeviceKeyFilter = %v, want %v", got, want)
	}
}

// TestFieldChange_BSON pins the stored shape: "" is kept as a value, nil and false are omitted.
func TestFieldChange_BSON(t *testing.T) {
	cases := []struct {
		name string
		in   FieldChange
		want bson.M
	}{
		{"value to value", FieldChange{From: "1.0", To: "1.1"}, bson.M{"from": "1.0", "to": "1.1"}},
		{"empty from", FieldChange{From: "", To: "1.1"}, bson.M{"from": "", "to": "1.1"}},
		{"unset before", FieldChange{To: "1.1"}, bson.M{"to": "1.1"}},
		{"masked", FieldChange{Changed: true}, bson.M{"changed": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := bson.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got bson.M
			if err := bson.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("stored = %v, want %v", got, tc.want)
			}
		})
	}
}
