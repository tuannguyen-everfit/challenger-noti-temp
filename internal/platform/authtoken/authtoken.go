// Package authtoken issues + verifies this service's JWT session tokens.
//
// An upstream identity provider verifies the user; this service is the session
// authority — it mints the access + refresh JWTs returned to clients and
// validates them on every authenticated request.
// Refresh-token rotation lives here too: verify the refresh JWT, mint a new
// pair, return.
package authtoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token type discriminator embedded in the JWT payload.
const (
	typeAccess  = "access"
	typeRefresh = "refresh"
)

// Signer mints + verifies HS256 JWTs for the mobile session.
type Signer struct {
	accessSecret  []byte
	refreshSecret []byte
	accessTTL     time.Duration
	refreshTTL    time.Duration
	now           func() time.Time
}

// Config holds the secrets + TTLs. AccessSecret + RefreshSecret are required.
type Config struct {
	AccessSecret  string
	RefreshSecret string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
}

// New builds a Signer.
func New(cfg Config) (*Signer, error) {
	if cfg.AccessSecret == "" || cfg.RefreshSecret == "" {
		return nil, errors.New("authtoken: access + refresh secrets required")
	}
	if cfg.AccessTTL <= 0 {
		cfg.AccessTTL = time.Hour
	}
	if cfg.RefreshTTL <= 0 {
		cfg.RefreshTTL = 30 * 24 * time.Hour
	}
	return &Signer{
		accessSecret:  []byte(cfg.AccessSecret),
		refreshSecret: []byte(cfg.RefreshSecret),
		accessTTL:     cfg.AccessTTL,
		refreshTTL:    cfg.RefreshTTL,
		now:           func() time.Time { return time.Now().UTC() },
	}, nil
}

// Pair is the mobile-facing token pair.
type Pair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int32
}

// Issue mints a fresh pair for the given user.
func (s *Signer) Issue(userID string) (Pair, error) {
	now := s.now()
	access, err := s.sign(s.accessSecret, claims{Sub: userID, Type: typeAccess, IAT: now.Unix(), EXP: now.Add(s.accessTTL).Unix()})
	if err != nil {
		return Pair{}, err
	}
	refresh, err := s.sign(s.refreshSecret, claims{Sub: userID, Type: typeRefresh, IAT: now.Unix(), EXP: now.Add(s.refreshTTL).Unix()})
	if err != nil {
		return Pair{}, err
	}
	return Pair{AccessToken: access, RefreshToken: refresh, ExpiresIn: int32(s.accessTTL.Seconds())}, nil
}

// ErrInvalidToken is returned when a token fails signature or claim checks.
var ErrInvalidToken = errors.New("authtoken: invalid token")

// ErrExpired is returned when the token's exp claim is in the past.
var ErrExpired = errors.New("authtoken: expired")

// VerifyAccess parses + validates an access JWT; returns the user_id (sub).
func (s *Signer) VerifyAccess(token string) (string, error) {
	return s.verify(token, s.accessSecret, typeAccess)
}

// VerifyRefresh parses + validates a refresh JWT; returns the user_id (sub).
func (s *Signer) VerifyRefresh(token string) (string, error) {
	return s.verify(token, s.refreshSecret, typeRefresh)
}

type claims struct {
	Sub  string `json:"sub"`
	Type string `json:"type"`
	IAT  int64  `json:"iat"`
	EXP  int64  `json:"exp"`
}

func (s *Signer) sign(secret []byte, c claims) (string, error) {
	header := `{"alg":"HS256","typ":"JWT"}`
	headerEnc := b64url([]byte(header))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("authtoken: marshal: %w", err)
	}
	payloadEnc := b64url(payload)
	signingInput := headerEnc + "." + payloadEnc
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	sig := b64url(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

func (s *Signer) verify(token string, secret []byte, wantType string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrInvalidToken
	}
	signingInput := parts[0] + "." + parts[1]
	expectedSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", ErrInvalidToken
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	if !hmac.Equal(mac.Sum(nil), expectedSig) {
		return "", ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", ErrInvalidToken
	}
	if c.Type != wantType || c.Sub == "" {
		return "", ErrInvalidToken
	}
	if c.EXP > 0 && s.now().Unix() > c.EXP {
		return "", ErrExpired
	}
	return c.Sub, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
