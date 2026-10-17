// Package auth verifies Firebase ID tokens sent by the Flutter site.
//
// Firebase signs ID tokens with rotating Google keys published as X.509
// certificates. Verification needs only the Firebase project ID, no service
// account, so the API can run anywhere.
package auth

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const googleCertsURL = "https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"

// ErrInvalidToken is returned for any token that fails verification.
var ErrInvalidToken = errors.New("invalid firebase id token")

// User is the identity carried by a verified token.
type User struct {
	UID           string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	Provider      string // e.g. "google.com" or "password"
}

// Verifier checks a raw ID token and returns the signed-in user.
type Verifier interface {
	Verify(ctx context.Context, idToken string) (*User, error)
}

// KeySource returns the current signing keys indexed by key ID.
type KeySource interface {
	Keys(ctx context.Context) (map[string]*rsa.PublicKey, error)
}

// FirebaseVerifier verifies tokens for one Firebase project.
type FirebaseVerifier struct {
	projectID string
	keys      KeySource
	now       func() time.Time
}

// NewFirebaseVerifier verifies tokens against Google's published keys.
func NewFirebaseVerifier(projectID string) *FirebaseVerifier {
	return NewFirebaseVerifierWithKeys(projectID, NewGoogleKeySource(http.DefaultClient))
}

// NewFirebaseVerifierWithKeys is used by tests to supply their own keys.
func NewFirebaseVerifierWithKeys(projectID string, keys KeySource) *FirebaseVerifier {
	return &FirebaseVerifier{projectID: projectID, keys: keys, now: time.Now}
}

type firebaseClaims struct {
	jwt.RegisteredClaims
	AuthTime      int64  `json:"auth_time"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	Firebase      struct {
		SignInProvider string `json:"sign_in_provider"`
	} `json:"firebase"`
}

func (v *FirebaseVerifier) Verify(ctx context.Context, idToken string) (*User, error) {
	keys, err := v.keys.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch signing keys: %w", err)
	}
	var claims firebaseClaims
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(v.projectID),
		jwt.WithIssuer("https://securetoken.google.com/"+v.projectID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(v.now),
	)
	_, err = parser.ParseWithClaims(idToken, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("unknown key id %q", kid)
		}
		return key, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if claims.Subject == "" || len(claims.Subject) > 128 {
		return nil, fmt.Errorf("%w: bad subject", ErrInvalidToken)
	}
	if claims.AuthTime > v.now().Add(30*time.Second).Unix() {
		return nil, fmt.Errorf("%w: auth_time in the future", ErrInvalidToken)
	}
	return &User{
		UID:           claims.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		Name:          claims.Name,
		Picture:       claims.Picture,
		Provider:      claims.Firebase.SignInProvider,
	}, nil
}

// GoogleKeySource fetches and caches Google's token signing certificates,
// refreshing them when the response's Cache-Control max-age runs out.
type GoogleKeySource struct {
	client *http.Client
	url    string

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

func NewGoogleKeySource(client *http.Client) *GoogleKeySource {
	return &GoogleKeySource{client: client, url: googleCertsURL}
}

var maxAgeRe = regexp.MustCompile(`max-age=(\d+)`)

func (s *GoogleKeySource) Keys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys != nil && time.Now().Before(s.expires) {
		return s.keys, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if s.keys != nil {
			return s.keys, nil // keep serving with the last good keys
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if s.keys != nil {
			return s.keys, nil
		}
		return nil, fmt.Errorf("google certs: status %d", resp.StatusCode)
	}
	var certs map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&certs); err != nil {
		return nil, fmt.Errorf("google certs: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(certs))
	for kid, certPEM := range certs {
		block, _ := pem.Decode([]byte(certPEM))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			keys[kid] = pub
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("google certs: no usable keys")
	}
	maxAge := time.Hour
	if m := maxAgeRe.FindStringSubmatch(resp.Header.Get("Cache-Control")); m != nil {
		if secs, err := strconv.Atoi(m[1]); err == nil {
			maxAge = time.Duration(secs) * time.Second
		}
	}
	s.keys = keys
	s.expires = time.Now().Add(maxAge)
	return keys, nil
}

// BearerToken extracts the token from an "Authorization: Bearer <token>" header.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
