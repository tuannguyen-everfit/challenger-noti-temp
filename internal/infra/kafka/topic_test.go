package kafka

import "testing"

func TestPrefixTopic(t *testing.T) {
	cases := []struct {
		prefix string
		topic  string
		want   string
	}{
		{"dev", "au-account-updated", "dev-au-account-updated"},
		{"staging", "au-account-updated", "staging-au-account-updated"},
		{"prod", "gb-profile-updated", "prod-gb-profile-updated"},
		{"", "au-account-updated", "au-account-updated"}, // no prefix → bare topic
	}
	for _, c := range cases {
		if got := PrefixTopic(c.prefix, c.topic); got != c.want {
			t.Errorf("PrefixTopic(%q, %q) = %q, want %q", c.prefix, c.topic, got, c.want)
		}
	}
}
