package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/emadaltaki/public-speaking-backend/internal/auth"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

// fakeVerifier accepts tokens of the form "uid" or "uid|email".
type fakeVerifier map[string]*auth.User

func (f fakeVerifier) Verify(_ context.Context, tok string) (*auth.User, error) {
	if u, ok := f[tok]; ok {
		return u, nil
	}
	return nil, auth.ErrInvalidToken
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	v := fakeVerifier{
		"alice": {UID: "alice", Email: "alice@example.com", Name: "Alice"},
		"admin": {UID: "admin", Email: "boss@example.com", EmailVerified: true},
	}
	s := NewServer(Config{
		AllowedOrigins: []string{"https://public-speaking-lemon.vercel.app"},
		AdminEmails:    []string{"boss@example.com"},
	}, store.NewMemory(), v, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s.Handler()
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestHealth(t *testing.T) {
	rec, body := do(t, newTestServer(t), "GET", "/healthz", "", nil)
	if rec.Code != 200 || body["status"] != "ok" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
	if db, _ := body["database"].(string); !strings.HasPrefix(db, "memory") {
		t.Fatalf("health should report the in-memory store, got %v", body)
	}
	if rec, _ := do(t, newTestServer(t), "GET", "/", "", nil); rec.Code != 200 {
		t.Fatalf("root: got %d", rec.Code)
	}
	if rec, _ := do(t, newTestServer(t), "GET", "/favicon.ico", "", nil); rec.Code != 204 {
		t.Fatalf("favicon: got %d", rec.Code)
	}
	if rec, _ := do(t, newTestServer(t), "GET", "/nope", "", nil); rec.Code != 404 {
		t.Fatalf("unknown path: got %d", rec.Code)
	}
}

func TestAuthRequired(t *testing.T) {
	h := newTestServer(t)
	if rec, _ := do(t, h, "GET", "/v1/me", "", nil); rec.Code != 401 {
		t.Fatalf("no token: got %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/v1/me", "bogus", nil); rec.Code != 401 {
		t.Fatalf("bad token: got %d", rec.Code)
	}
}

func TestMe(t *testing.T) {
	h := newTestServer(t)
	rec, body := do(t, h, "GET", "/v1/me", "alice", nil)
	if rec.Code != 200 || body["uid"] != "alice" || body["displayName"] != "Alice" || body["language"] != "en" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
	rec, body = do(t, h, "PATCH", "/v1/me", "alice", map[string]any{"displayName": " Al ", "language": "ar"})
	if rec.Code != 200 || body["displayName"] != "Al" || body["language"] != "ar" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
	// A later sign-in must not overwrite the chosen name.
	_, body = do(t, h, "GET", "/v1/me", "alice", nil)
	if body["displayName"] != "Al" {
		t.Fatalf("name overwritten: %v", body)
	}
	if rec, _ := do(t, h, "PATCH", "/v1/me", "alice", map[string]any{"language": "fr"}); rec.Code != 400 {
		t.Fatalf("bad language: got %d", rec.Code)
	}
}

func TestGames(t *testing.T) {
	h := newTestServer(t)
	for _, s := range []map[string]any{
		{"gameId": "triple-step", "durationSeconds": 60, "score": 3, "details": map[string]any{"topic": "travel"}},
		{"gameId": "triple-step", "durationSeconds": 90, "score": 5},
		{"gameId": "conductor", "durationSeconds": 120},
	} {
		if rec, body := do(t, h, "POST", "/v1/games/sessions", "alice", s); rec.Code != 201 {
			t.Fatalf("add session: %d %v", rec.Code, body)
		}
	}
	if rec, _ := do(t, h, "POST", "/v1/games/sessions", "alice", map[string]any{"gameId": "Bad Id!"}); rec.Code != 400 {
		t.Fatalf("bad game id: got %d", rec.Code)
	}
	if rec, _ := do(t, h, "POST", "/v1/games/sessions", "alice", map[string]any{"gameId": "podcast", "details": []int{1}}); rec.Code != 400 {
		t.Fatalf("array details: got %d", rec.Code)
	}

	_, body := do(t, h, "GET", "/v1/games/sessions?gameId=triple-step", "alice", nil)
	if n := len(body["sessions"].([]any)); n != 2 {
		t.Fatalf("want 2 triple-step sessions, got %d", n)
	}

	_, body = do(t, h, "GET", "/v1/games/progress", "alice", nil)
	games := body["games"].([]any)
	if len(games) != 2 {
		t.Fatalf("want 2 games, got %v", games)
	}
	for _, g := range games {
		g := g.(map[string]any)
		if g["gameId"] == "triple-step" && (g["plays"] != 2.0 || g["totalSeconds"] != 150.0 || g["bestScore"] != 5.0) {
			t.Fatalf("triple-step progress wrong: %v", g)
		}
	}

	// Another user sees none of it.
	_, body = do(t, h, "GET", "/v1/games/progress", "admin", nil)
	if len(body["games"].([]any)) != 0 {
		t.Fatalf("progress leaked across users: %v", body)
	}
}

func TestBookings(t *testing.T) {
	h := newTestServer(t)
	booking := map[string]any{"name": "Sam", "contact": "sam@example.com", "preferredTime": "Tuesday evening", "reason": "Job interview prep"}

	if rec, body := do(t, h, "POST", "/v1/bookings", "", booking); rec.Code != 201 || body["status"] != "new" || body["reason"] != "Job interview prep" {
		t.Fatalf("anonymous booking: %d %v", rec.Code, body)
	}
	if rec, body := do(t, h, "POST", "/v1/bookings", "alice", booking); rec.Code != 201 || body["uid"] != "alice" {
		t.Fatalf("signed-in booking: %d %v", rec.Code, body)
	}
	if rec, _ := do(t, h, "POST", "/v1/bookings", "", map[string]any{"name": "Sam", "contact": "s@x.com", "preferredTime": "Mon"}); rec.Code != 400 {
		t.Fatalf("missing fields: got %d", rec.Code)
	}

	_, body := do(t, h, "GET", "/v1/bookings", "alice", nil)
	if n := len(body["bookings"].([]any)); n != 1 {
		t.Fatalf("alice should see 1 booking, got %d", n)
	}

	if rec, _ := do(t, h, "GET", "/v1/admin/bookings", "alice", nil); rec.Code != 403 {
		t.Fatalf("non-admin: got %d", rec.Code)
	}
	_, body = do(t, h, "GET", "/v1/admin/bookings", "admin", nil)
	list := body["bookings"].([]any)
	if len(list) != 2 {
		t.Fatalf("admin should see 2 bookings, got %d", len(list))
	}
	id := int(list[0].(map[string]any)["id"].(float64))
	rec, body := do(t, h, "PATCH", "/v1/admin/bookings/"+strconv.Itoa(id), "admin", map[string]any{"status": "contacted"})
	if rec.Code != 200 || body["status"] != "contacted" {
		t.Fatalf("set status: %d %v", rec.Code, body)
	}
	if rec, _ := do(t, h, "PATCH", "/v1/admin/bookings/999", "admin", map[string]any{"status": "closed"}); rec.Code != 404 {
		t.Fatalf("missing booking: got %d", rec.Code)
	}
}

func TestBookingRateLimit(t *testing.T) {
	h := newTestServer(t)
	booking := map[string]any{"name": "Sam", "contact": "sam@example.com", "preferredTime": "Monday", "reason": "Wedding speech"}
	var last int
	for i := 0; i < 6; i++ {
		rec, _ := do(t, h, "POST", "/v1/bookings", "", booking)
		last = rec.Code
	}
	if last != 429 {
		t.Fatalf("6th booking from one address: got %d, want 429", last)
	}
}

func TestCORS(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest("OPTIONS", "/v1/me", nil)
	req.Header.Set("Origin", "https://public-speaking-lemon.vercel.app")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "https://public-speaking-lemon.vercel.app" {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}

	req = httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unknown origin was allowed")
	}
}
