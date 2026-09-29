package authtoken

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func newSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := New(Config{
		AccessSecret:  "access-secret-xxx",
		RefreshSecret: "refresh-secret-yyy",
		AccessTTL:     time.Hour,
		RefreshTTL:    24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNew_RejectsEmptySecrets(t *testing.T) {
	if _, err := New(Config{RefreshSecret: "x"}); err == nil {
		t.Errorf("missing access secret: want error")
	}
	if _, err := New(Config{AccessSecret: "x"}); err == nil {
		t.Errorf("missing refresh secret: want error")
	}
}

func TestIssueVerify_AccessRoundtrip(t *testing.T) {
	s := newSigner(t)
	pair, err := s.Issue("user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("empty tokens")
	}
	if pair.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d, want 3600", pair.ExpiresIn)
	}
	got, err := s.VerifyAccess(pair.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccess: %v", err)
	}
	if got != "user-1" {
		t.Errorf("sub = %q, want user-1", got)
	}
}

func TestVerifyRefresh_Roundtrip(t *testing.T) {
	s := newSigner(t)
	pair, _ := s.Issue("user-2")
	got, err := s.VerifyRefresh(pair.RefreshToken)
	if err != nil {
		t.Fatalf("VerifyRefresh: %v", err)
	}
	if got != "user-2" {
		t.Errorf("sub = %q, want user-2", got)
	}
}

func TestVerifyAccess_RejectsRefreshToken(t *testing.T) {
	s := newSigner(t)
	pair, _ := s.Issue("u")
	if _, err := s.VerifyAccess(pair.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (refresh token used as access)", err)
	}
}

func TestVerifyRefresh_RejectsAccessToken(t *testing.T) {
	s := newSigner(t)
	pair, _ := s.Issue("u")
	if _, err := s.VerifyRefresh(pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (access token used as refresh)", err)
	}
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	a := newSigner(t)
	pair, _ := a.Issue("u")
	other, _ := New(Config{AccessSecret: "DIFFERENT-1", RefreshSecret: "DIFFERENT-2"})
	if _, err := other.VerifyAccess(pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (wrong secret)", err)
	}
}

func TestVerify_RejectsTampered(t *testing.T) {
	s := newSigner(t)
	pair, _ := s.Issue("u")
	parts := strings.Split(pair.AccessToken, ".")
	tampered := parts[0] + ".XXX." + parts[2]
	if _, err := s.VerifyAccess(tampered); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (tampered payload)", err)
	}
}

func TestVerify_RejectsMalformed(t *testing.T) {
	s := newSigner(t)
	for _, bad := range []string{"", "not-a-token", "one.two", "a.b.c.d"} {
		if _, err := s.VerifyAccess(bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%q: err = %v, want ErrInvalidToken", bad, err)
		}
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	s := newSigner(t)
	past := time.Now().UTC().Add(-2 * time.Hour)
	s.now = func() time.Time { return past }
	pair, _ := s.Issue("u")
	s.now = func() time.Time { return time.Now().UTC() } // restore real clock
	if _, err := s.VerifyAccess(pair.AccessToken); !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v, want ErrExpired", err)
	}
}
