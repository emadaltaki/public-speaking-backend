package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emadaltaki/public-speaking-backend/internal/auth"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

func TestDailyStreak(t *testing.T) {
	loc, _ := time.LoadLocation("America/Toronto")
	now := time.Date(2026, 10, 18, 20, 0, 0, 0, loc)
	at := func(daysAgo, hour int) store.GameSession {
		return store.GameSession{PlayedAt: time.Date(2026, 10, 18-daysAgo, hour, 0, 0, 0, loc)}
	}
	cases := []struct {
		name        string
		sessions    []store.GameSession
		streak      int
		playedToday bool
		todayRounds int
	}{
		{"none", nil, 0, false, 0},
		{"today only", []store.GameSession{at(0, 9)}, 1, true, 1},
		{"yesterday only", []store.GameSession{at(1, 9)}, 1, false, 0},
		{"three days", []store.GameSession{at(0, 9), at(1, 23), at(2, 1)}, 3, true, 1},
		{"gap", []store.GameSession{at(0, 9), at(2, 9)}, 1, true, 1},
		{"two today", []store.GameSession{at(0, 9), at(0, 10), at(1, 9)}, 2, true, 2},
		// 23:30 Toronto is the next day in UTC; it still counts as that local day.
		{"late night", []store.GameSession{{PlayedAt: time.Date(2026, 10, 17, 23, 30, 0, 0, loc).UTC()}}, 1, false, 0},
	}
	for _, c := range cases {
		streak, today, rounds := dailyStreak(c.sessions, loc, now)
		if streak != c.streak || today != c.playedToday || rounds != c.todayRounds {
			t.Errorf("%s: got %d %v %d, want %d %v %d", c.name, streak, today, rounds, c.streak, c.playedToday, c.todayRounds)
		}
	}
}

type streakEnv struct {
	h  http.Handler
	st *store.Memory
	m  *fakeMailer
}

func newStreakEnv(t *testing.T, mode string, testList ...string) streakEnv {
	t.Helper()
	st := store.NewMemory()
	m := &fakeMailer{}
	v := fakeVerifier{
		"ada": {UID: "ada", Email: "ada@example.com", EmailVerified: true, Name: "Ada Lovelace"},
		"bob": {UID: "bob", Email: "bob@example.com", EmailVerified: true, Name: "Bob"},
		"pw":  {UID: "pw", Email: "pw@example.com"},
	}
	s := NewServer(Config{
		Mailer:          m,
		CronSecret:      "cron-secret",
		EmailMode:       mode,
		EmailTestList:   testList,
		DefaultTimeZone: "America/Toronto",
		SiteURL:         "https://site.example",
		PublicURL:       "https://api.example",
	}, st, v, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return streakEnv{s.Handler(), st, m}
}

func (e streakEnv) subjects() []string {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	var out []string
	for _, s := range e.m.sent {
		out = append(out, s.to+": "+s.subject)
	}
	return out
}

func TestWelcomeEmail(t *testing.T) {
	e := newStreakEnv(t, "on")
	// Google sign-in: verified on first call, welcomed once.
	do(t, e.h, "GET", "/v1/me", "ada", nil)
	do(t, e.h, "GET", "/v1/me", "ada", nil)
	if got := e.subjects(); len(got) != 1 || !strings.Contains(got[0], "ada@example.com: Welcome to Speak Up Fearless") {
		t.Fatalf("welcome emails: %v", got)
	}
	if !strings.Contains(e.m.sent[0].html, "Hi Ada,") || !strings.Contains(e.m.sent[0].html, "/v1/email/unsubscribe?u=ada&amp;t=") {
		t.Fatalf("welcome email content missing greeting or unsubscribe link")
	}

	// Email sign-up: welcomed after entering the code.
	do(t, e.h, "POST", "/v1/me/verify/send", "pw", nil)
	code := e.m.lastCode(t)
	do(t, e.h, "POST", "/v1/me/verify/check", "pw", map[string]string{"code": code})
	got := e.subjects()
	if len(got) != 3 || !strings.Contains(got[2], "pw@example.com: Welcome") {
		t.Fatalf("after code: %v", got)
	}
}

func TestEmailModes(t *testing.T) {
	off := newStreakEnv(t, "off")
	do(t, off.h, "GET", "/v1/me", "ada", nil)
	if got := off.subjects(); len(got) != 0 {
		t.Fatalf("mode off sent: %v", got)
	}
	test := newStreakEnv(t, "test", "BOB@example.com")
	do(t, test.h, "GET", "/v1/me", "ada", nil)
	do(t, test.h, "GET", "/v1/me", "bob", nil)
	if got := test.subjects(); len(got) != 1 || !strings.HasPrefix(got[0], "bob@example.com") {
		t.Fatalf("test mode should only email the allowlist: %v", got)
	}
}

func TestStreakStartEmail(t *testing.T) {
	e := newStreakEnv(t, "on")
	do(t, e.h, "GET", "/v1/me", "ada", nil) // welcome
	do(t, e.h, "POST", "/v1/games/sessions", "ada", map[string]any{"gameId": "triple-step", "durationSeconds": 60})
	if n := len(e.subjects()); n != 1 {
		t.Fatalf("other games shouldn't email: %v", e.subjects())
	}
	do(t, e.h, "POST", "/v1/games/sessions", "ada", map[string]any{"gameId": "daily", "durationSeconds": 60})
	do(t, e.h, "POST", "/v1/games/sessions", "ada", map[string]any{"gameId": "daily", "durationSeconds": 60})
	got := e.subjects()
	if len(got) != 2 || !strings.Contains(got[1], "You started a streak") {
		t.Fatalf("streak start: %v", got)
	}

	// Bob already played yesterday, so today continues a streak: no email.
	do(t, e.h, "GET", "/v1/me", "bob", nil)
	_, body := do(t, e.h, "POST", "/v1/games/sessions", "bob", map[string]any{"gameId": "daily", "durationSeconds": 60})
	e.st.SetPlayedAt(int64(body["id"].(float64)), time.Now().Add(-24*time.Hour))
	before := len(e.subjects())
	do(t, e.h, "POST", "/v1/games/sessions", "bob", map[string]any{"gameId": "daily", "durationSeconds": 60})
	if got := e.subjects(); len(got) != before {
		t.Fatalf("continuing streak shouldn't email: %v", got[before:])
	}
}

func cron(t *testing.T, h http.Handler, secret, now string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/cron/streak-reminders?now="+now, nil)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = jsonDecode(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestStreakReminders(t *testing.T) {
	e := newStreakEnv(t, "test", "ada@example.com", "bob@example.com")
	loc, _ := time.LoadLocation("America/Toronto")
	evening := time.Now().In(loc)
	evening = time.Date(evening.Year(), evening.Month(), evening.Day(), 19, 30, 0, 0, loc)
	afternoon := evening.Add(-5 * time.Hour)

	play := func(token string, at time.Time) {
		_, body := do(t, e.h, "POST", "/v1/games/sessions", token, map[string]any{"gameId": "daily", "durationSeconds": 60})
		e.st.SetPlayedAt(int64(body["id"].(float64)), at)
	}
	do(t, e.h, "GET", "/v1/me", "ada", nil)
	do(t, e.h, "GET", "/v1/me", "bob", nil)
	// Ada played the two days before today; Bob already played today.
	play("ada", evening.AddDate(0, 0, -2))
	play("ada", evening.AddDate(0, 0, -1))
	play("bob", evening.AddDate(0, 0, -1))
	play("bob", evening.Add(-time.Hour))
	e.m.sent = nil

	if code, _ := cron(t, e.h, "", evening.Format(time.RFC3339)); code != 401 {
		t.Fatalf("no secret: %d", code)
	}
	if code, _ := cron(t, e.h, "wrong", evening.Format(time.RFC3339)); code != 401 {
		t.Fatalf("wrong secret: %d", code)
	}
	if _, res := cron(t, e.h, "cron-secret", afternoon.Format(time.RFC3339)); res["sent"] != 0.0 || res["notYet"] != 2.0 {
		t.Fatalf("afternoon run: %v", res)
	}
	_, res := cron(t, e.h, "cron-secret", evening.Format(time.RFC3339))
	if res["sent"] != 1.0 || res["playedToday"] != 1.0 {
		t.Fatalf("evening run: %v", res)
	}
	got := e.subjects()
	if len(got) != 1 || !strings.Contains(got[0], "ada@example.com: ⏰ Your 2-day streak ends at midnight") {
		t.Fatalf("reminder: %v", got)
	}
	if _, res := cron(t, e.h, "cron-secret", evening.Add(time.Hour).Format(time.RFC3339)); res["sent"] != 0.0 {
		t.Fatalf("second run must not resend: %v", res)
	}

	// The unsubscribe link in the email turns streak emails off.
	link := regexp.MustCompile(`href="(https://api\.example/v1/email/unsubscribe[^"]+)"`).FindStringSubmatch(e.m.sent[0].html)
	if link == nil || e.m.sent[0].headers["List-Unsubscribe"] == "" {
		t.Fatalf("reminder has no unsubscribe link or header")
	}
	path := strings.ReplaceAll(strings.TrimPrefix(link[1], "https://api.example"), "&amp;", "&")
	rec, _ := do(t, e.h, "GET", path, "", nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "https://site.example/account?emails=off" {
		t.Fatalf("unsubscribe: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if _, body := do(t, e.h, "GET", "/v1/me", "ada", nil); body["streakEmails"] != false {
		t.Fatalf("streakEmails still on: %v", body["streakEmails"])
	}
	if rec, _ := do(t, e.h, "GET", "/v1/email/unsubscribe?u=bob&t=forged", "", nil); rec.Header().Get("Location") != "https://site.example/account?emails=invalid" {
		t.Fatalf("forged unsubscribe: %s", rec.Header().Get("Location"))
	}
}

func TestProfileTimeZoneAndEmails(t *testing.T) {
	e := newStreakEnv(t, "off")
	_, body := do(t, e.h, "GET", "/v1/me", "ada", nil)
	if body["streakEmails"] != true || body["timeZone"] != "" {
		t.Fatalf("defaults: %v", body)
	}
	_, body = do(t, e.h, "PATCH", "/v1/me", "ada", map[string]any{"timeZone": "Asia/Dubai", "streakEmails": false})
	if body["timeZone"] != "Asia/Dubai" || body["streakEmails"] != false {
		t.Fatalf("patch: %v", body)
	}
	rec, body := do(t, e.h, "PATCH", "/v1/me", "ada", map[string]any{"timeZone": "Mars/Olympus"})
	if rec.Code != 200 || body["timeZone"] != "Asia/Dubai" {
		t.Fatalf("invalid zone should be ignored: %d %v", rec.Code, body)
	}
}

var _ auth.Verifier = fakeVerifier{}

var jsonDecode = json.Unmarshal
