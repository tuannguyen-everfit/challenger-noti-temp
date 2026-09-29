package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubVerifier struct {
	wantToken string
	respUser  string
	respErr   error
}

func (s *stubVerifier) VerifyAccess(token string) (string, error) {
	if s.wantToken != "" && token != s.wantToken {
		return "", errors.New("token mismatch")
	}
	return s.respUser, s.respErr
}

func mkReq(authHeader string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	return r
}

func TestBearerAuth_NilVerifier_FailsClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	BearerAuth(nil)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Errorf("downstream should not run when verifier is nil")
	})).ServeHTTP(rec, mkReq("Bearer xxx"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestBearerAuth_MissingHeader_401(t *testing.T) {
	v := &stubVerifier{respUser: "u"}
	rec := httptest.NewRecorder()
	BearerAuth(v)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Errorf("downstream should not run")
	})).ServeHTTP(rec, mkReq(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want Bearer realm", got)
	}
	if !strings.Contains(rec.Body.String(), "AUTH_TOKEN_MISSING") {
		t.Errorf("body missing code: %s", rec.Body.String())
	}
}

func TestBearerAuth_WrongScheme_401(t *testing.T) {
	v := &stubVerifier{respUser: "u"}
	rec := httptest.NewRecorder()
	BearerAuth(v)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})).
		ServeHTTP(rec, mkReq("Basic dXNlcjpwYXNz"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestBearerAuth_InvalidToken_401(t *testing.T) {
	v := &stubVerifier{respErr: errors.New("bad sig")}
	rec := httptest.NewRecorder()
	BearerAuth(v)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})).
		ServeHTTP(rec, mkReq("Bearer xxx"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "AUTH_TOKEN_INVALID") {
		t.Errorf("body missing code: %s", rec.Body.String())
	}
}

func TestBearerAuth_Expired_401_WithExpiredCode(t *testing.T) {
	expiredErr := errors.New("expired")
	SetExpiredSentinel(expiredErr)
	t.Cleanup(func() { SetExpiredSentinel(errors.New("middleware: token expired (placeholder)")) })

	v := &stubVerifier{respErr: expiredErr}
	rec := httptest.NewRecorder()
	BearerAuth(v)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})).
		ServeHTTP(rec, mkReq("Bearer xxx"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "AUTH_TOKEN_EXPIRED") {
		t.Errorf("body missing expired code: %s", rec.Body.String())
	}
}

func TestBearerAuth_Valid_StampsUserID(t *testing.T) {
	v := &stubVerifier{wantToken: "good-token", respUser: "user-42"}
	called := false
	rec := httptest.NewRecorder()
	BearerAuth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := UserIDFromContext(r.Context()); got != "user-42" {
			t.Errorf("UserIDFromContext = %q, want user-42", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, mkReq("Bearer good-token"))
	if !called {
		t.Fatalf("downstream not invoked")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestUserIDFromContext_AbsentReturnsEmpty(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := UserIDFromContext(r.Context()); got != "" {
		t.Errorf("UserIDFromContext (no middleware) = %q, want empty", got)
	}
}

func TestWithUserID_RoundTrips(t *testing.T) {
	ctx := WithUserID(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "u1")
	if got := UserIDFromContext(ctx); got != "u1" {
		t.Errorf("UserIDFromContext = %q, want u1", got)
	}
}
