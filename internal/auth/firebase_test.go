package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type staticKeys map[string]*rsa.PublicKey

func (k staticKeys) Keys(context.Context) (map[string]*rsa.PublicKey, error) { return k, nil }

const project = "speakup-ca500"

func signToken(t *testing.T, key *rsa.PrivateKey, kid string, edit func(jwt.MapClaims)) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":            "https://securetoken.google.com/" + project,
		"aud":            project,
		"sub":            "user-123",
		"iat":            now.Add(-time.Minute).Unix(),
		"exp":            now.Add(time.Hour).Unix(),
		"auth_time":      now.Add(-time.Minute).Unix(),
		"email":          "a@example.com",
		"email_verified": true,
		"name":           "Ada",
		"firebase":       map[string]any{"sign_in_provider": "google.com"},
	}
	if edit != nil {
		edit(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerify(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	v := NewFirebaseVerifierWithKeys(project, staticKeys{"k1": &key.PublicKey})

	u, err := v.Verify(context.Background(), signToken(t, key, "k1", nil))
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if u.UID != "user-123" || u.Email != "a@example.com" || !u.EmailVerified || u.Provider != "google.com" {
		t.Fatalf("unexpected user: %+v", u)
	}

	bad := map[string]string{
		"wrong audience": signToken(t, key, "k1", func(c jwt.MapClaims) { c["aud"] = "other-project" }),
		"wrong issuer":   signToken(t, key, "k1", func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }),
		"expired":        signToken(t, key, "k1", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"no subject":     signToken(t, key, "k1", func(c jwt.MapClaims) { c["sub"] = "" }),
		"future auth":    signToken(t, key, "k1", func(c jwt.MapClaims) { c["auth_time"] = time.Now().Add(time.Hour).Unix() }),
		"unknown kid":    signToken(t, key, "k2", nil),
		"wrong key":      signToken(t, other, "k1", nil),
		"garbage":        "not-a-token",
	}
	for name, tok := range bad {
		if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
}
