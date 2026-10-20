package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emadaltaki/public-speaking-backend/internal/auth"
	"github.com/emadaltaki/public-speaking-backend/internal/mail"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

type sentMail struct {
	to, subject, body, html string
	headers                 map[string]string
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
	fail bool
}

func (m *fakeMailer) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return io.ErrUnexpectedEOF
	}
	m.sent = append(m.sent, sentMail{msg.To, msg.Subject, msg.Text, msg.HTML, msg.Headers})
	return nil
}

func (m *fakeMailer) lastCode(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("no email sent")
	}
	code := regexp.MustCompile(`[0-9]{6}`).FindString(m.sent[len(m.sent)-1].body)
	if code == "" {
		t.Fatal("no code in email")
	}
	return code
}

func newVerifyServer(t *testing.T, m *fakeMailer) (http.Handler, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	v := fakeVerifier{
		"pw":     {UID: "pw", Email: "pw@example.com"},
		"google": {UID: "google", Email: "g@example.com", EmailVerified: true},
	}
	cfg := Config{}
	if m != nil {
		cfg.Mailer = m
	}
	return NewServer(cfg, st, v, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler(), st
}

func TestVerifyGoogleIsAlreadyVerified(t *testing.T) {
	m := &fakeMailer{}
	h, _ := newVerifyServer(t, m)
	_, body := do(t, h, "GET", "/v1/me", "google", nil)
	if body["emailVerified"] != true {
		t.Fatalf("google user not verified: %v", body)
	}
	rec, body := do(t, h, "POST", "/v1/me/verify/send", "google", nil)
	if rec.Code != 200 || body["verified"] != true || len(m.sent) != 0 {
		t.Fatalf("send for google user: %d %v, %d mails", rec.Code, body, len(m.sent))
	}
}

func TestVerifyFlow(t *testing.T) {
	m := &fakeMailer{}
	h, _ := newVerifyServer(t, m)

	if _, body := do(t, h, "GET", "/v1/me", "pw", nil); body["emailVerified"] != false {
		t.Fatalf("new password user should be unverified: %v", body)
	}
	rec, body := do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	if rec.Code != 200 || body["sent"] != true {
		t.Fatalf("send: %d %v", rec.Code, body)
	}
	if m.sent[0].to != "pw@example.com" || !strings.HasPrefix(m.sent[0].subject, "Your speakUnshaken code: ") ||
		!strings.Contains(m.sent[0].body, "It expires in 10 minutes. If you didn't sign up, you can ignore this email.") {
		t.Fatalf("unexpected email: %+v", m.sent[0])
	}
	code := m.lastCode(t)
	if !strings.HasSuffix(m.sent[0].subject, code) {
		t.Fatalf("subject should carry the code: %q", m.sent[0].subject)
	}

	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	rec, body = do(t, h, "POST", "/v1/me/verify/check", "pw", map[string]string{"code": wrong})
	if rec.Code != 400 || body["error"] != "wrong" || body["attemptsLeft"] != 4.0 {
		t.Fatalf("wrong code: %d %v", rec.Code, body)
	}
	rec, body = do(t, h, "POST", "/v1/me/verify/check", "pw", map[string]string{"code": code})
	if rec.Code != 200 || body["verified"] != true {
		t.Fatalf("right code: %d %v", rec.Code, body)
	}
	if _, body := do(t, h, "GET", "/v1/me", "pw", nil); body["emailVerified"] != true {
		t.Fatalf("not verified after check: %v", body)
	}
	// The code is used up; sending again just reports verified.
	if _, body := do(t, h, "POST", "/v1/me/verify/send", "pw", nil); body["verified"] != true {
		t.Fatalf("send after verify: %v", body)
	}
}

func TestVerifyAttemptsRunOut(t *testing.T) {
	m := &fakeMailer{}
	h, _ := newVerifyServer(t, m)
	do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	code := m.lastCode(t)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 4; i >= 0; i-- {
		_, body := do(t, h, "POST", "/v1/me/verify/check", "pw", map[string]string{"code": wrong})
		if body["attemptsLeft"] != float64(i) {
			t.Fatalf("attempt %d: %v", 5-i, body)
		}
	}
	rec, body := do(t, h, "POST", "/v1/me/verify/check", "pw", map[string]string{"code": code})
	if rec.Code != 400 || body["error"] != "expired" {
		t.Fatalf("right code after 5 misses should be expired: %d %v", rec.Code, body)
	}
}

func TestVerifyExpired(t *testing.T) {
	m := &fakeMailer{}
	h, st := newVerifyServer(t, m)
	do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	code := m.lastCode(t)
	c, _ := st.GetEmailCode(context.Background(), "pw")
	c.ExpiresAt = time.Now().Add(-time.Second)
	_ = st.PutEmailCode(context.Background(), c)
	rec, body := do(t, h, "POST", "/v1/me/verify/check", "pw", map[string]string{"code": code})
	if rec.Code != 400 || body["error"] != "expired" {
		t.Fatalf("expired code: %d %v", rec.Code, body)
	}
	if rec, body := do(t, h, "POST", "/v1/me/verify/check", "google", map[string]string{"code": "123456"}); rec.Code != 200 || body["verified"] != true {
		t.Fatalf("verified user check: %d %v", rec.Code, body)
	}
}

func TestVerifySendRateLimit(t *testing.T) {
	m := &fakeMailer{}
	h, _ := newVerifyServer(t, m)
	do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	rec, body := do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	if rec.Code != 429 || body["retryAfter"] == nil || body["retryAfter"].(float64) < 55 {
		t.Fatalf("second send within a minute: %d %v", rec.Code, body)
	}
	if len(m.sent) != 1 {
		t.Fatalf("want 1 email, got %d", len(m.sent))
	}
}

func TestVerifyHourlyLimit(t *testing.T) {
	st := store.NewMemory()
	m := &fakeMailer{}
	s := NewServer(Config{Mailer: m}, st, fakeVerifier{"pw": {UID: "pw", Email: "pw@example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := s.Handler()
	do(t, h, "GET", "/v1/me", "pw", nil)
	// Five sends spread over the last hour, the newest over a minute ago.
	for i := 0; i < 5; i++ {
		_ = st.RecordCodeSend(context.Background(), "pw")
	}
	st.ShiftSends("pw", -2*time.Minute)
	rec, body := do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	if rec.Code != 429 || body["retryAfter"].(float64) < 3000 {
		t.Fatalf("sixth send in an hour: %d %v", rec.Code, body)
	}
}

func TestVerifyNotConfigured(t *testing.T) {
	h, _ := newVerifyServer(t, nil)
	rec, body := do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	if rec.Code != 503 || body["error"] != "email not configured" {
		t.Fatalf("no mailer: %d %v", rec.Code, body)
	}
}

func TestVerifyArabicEmail(t *testing.T) {
	m := &fakeMailer{}
	h, _ := newVerifyServer(t, m)
	do(t, h, "PATCH", "/v1/me", "pw", map[string]any{"language": "ar"})
	do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	if !strings.Contains(m.sent[0].body, "10 دقائق") {
		t.Fatalf("expected Arabic email, got %q", m.sent[0].body)
	}
}

func TestVerifyMailFailure(t *testing.T) {
	m := &fakeMailer{fail: true}
	h, _ := newVerifyServer(t, m)
	if rec, _ := do(t, h, "POST", "/v1/me/verify/send", "pw", nil); rec.Code != 502 {
		t.Fatalf("mail failure: got %d", rec.Code)
	}
	// A failed send doesn't count, so an immediate retry is allowed.
	m.fail = false
	if rec, body := do(t, h, "POST", "/v1/me/verify/send", "pw", nil); rec.Code != 200 {
		t.Fatalf("retry after failure: %d %v", rec.Code, body)
	}
}

var _ auth.Verifier = fakeVerifier{}

func TestVerifyLinkOneTap(t *testing.T) {
	m := &fakeMailer{}
	h, _ := newVerifyServer(t, m)
	do(t, h, "POST", "/v1/me/verify/send", "pw", nil)
	code := m.lastCode(t)
	link := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(m.sent[0].html)
	if link == nil || !strings.Contains(link[1], "/v1/verify/link?u=pw&amp;c="+code) {
		t.Fatalf("email has no verify button link: %v", link)
	}

	// A wrong code in the link doesn't verify.
	rec, _ := do(t, h, "GET", "/v1/verify/link?u=pw&c=999999", "", nil)
	if code == "999999" {
		rec, _ = do(t, h, "GET", "/v1/verify/link?u=pw&c=888888", "", nil)
	}
	if rec.Code != 303 || !strings.HasSuffix(rec.Header().Get("Location"), "/login?verify=wrong") {
		t.Fatalf("wrong link: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// The real link verifies without signing in, then sends people to the site.
	rec, _ = do(t, h, "GET", "/v1/verify/link?u=pw&c="+code, "", nil)
	if rec.Code != 303 || !strings.HasSuffix(rec.Header().Get("Location"), "/login?verify=verified") {
		t.Fatalf("good link: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if _, body := do(t, h, "GET", "/v1/me", "pw", nil); body["emailVerified"] != true {
		t.Fatalf("not verified after link: %v", body)
	}
	if rec, _ := do(t, h, "GET", "/v1/verify/link?u=nobody&c=123456", "", nil); !strings.HasSuffix(rec.Header().Get("Location"), "verify=expired") {
		t.Fatalf("unknown user link: %s", rec.Header().Get("Location"))
	}
}
