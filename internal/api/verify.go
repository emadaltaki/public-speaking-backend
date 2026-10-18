package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/emadaltaki/public-speaking-backend/internal/mail"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

const (
	codeTTL         = 10 * time.Minute
	maxCodeAttempts = 5
	sendCooldown    = 60 * time.Second
	maxSendsPerHour = 5
)

var codeRe = regexp.MustCompile(`^[0-9]{6}$`)

func hashCode(code, uid string) string {
	sum := sha256.Sum256([]byte(code + uid))
	return hex.EncodeToString(sum[:])
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// sendVerifyCode emails a 6-digit code to the signed-in user, unless their
// email is already verified (Google sign-ins always are).
func (s *Server) sendVerifyCode(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
		return
	}
	if s.cfg.Mailer == nil {
		writeError(w, http.StatusServiceUnavailable, "email not configured")
		return
	}
	if u.Email == "" {
		writeError(w, http.StatusBadRequest, "this account has no email address")
		return
	}

	now := time.Now()
	sends, err := s.store.CodeSendsSince(r.Context(), u.UID, now.Add(-time.Hour))
	if err != nil {
		s.serverError(w, "code sends", err)
		return
	}
	if len(sends) > 0 {
		if wait := sendCooldown - now.Sub(sends[0]); wait > 0 {
			tooMany(w, wait)
			return
		}
	}
	if len(sends) >= maxSendsPerHour {
		tooMany(w, sends[maxSendsPerHour-1].Add(time.Hour).Sub(now))
		return
	}

	code, err := newCode()
	if err != nil {
		s.serverError(w, "new code", err)
		return
	}
	if err := s.store.PutEmailCode(r.Context(), store.EmailCode{
		UID:       u.UID,
		Email:     u.Email,
		CodeHash:  hashCode(code, u.UID),
		ExpiresAt: now.Add(codeTTL),
	}); err != nil {
		s.serverError(w, "put email code", err)
		return
	}
	msg := codeEmail(code, s.verifyURL(u.UID, code), u.Language)
	msg.To = u.Email
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cfg.Mailer.Send(ctx, msg); err != nil {
		s.log.Error("send code email", "err", err)
		writeError(w, http.StatusBadGateway, "could not send email")
		return
	}
	// Only emails that actually went out count towards the limits, so a
	// mail-server hiccup doesn't lock the visitor out.
	if err := s.store.RecordCodeSend(r.Context(), u.UID); err != nil {
		s.log.Error("record code send", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(wait.Seconds() + 0.999)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", fmt.Sprint(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":      "too many codes requested",
		"retryAfter": secs,
	})
}

// verifyCode checks code against the user's pending code and marks the
// email verified on a match. result is "verified", "expired" or "wrong".
func (s *Server) verifyCode(ctx context.Context, u store.User, code string) (result string, attemptsLeft int, err error) {
	if u.EmailVerified {
		return "verified", 0, nil
	}
	c, err := s.store.GetEmailCode(ctx, u.UID)
	if errors.Is(err, store.ErrNotFound) {
		return "expired", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if time.Now().After(c.ExpiresAt) || c.Attempts >= maxCodeAttempts || c.Email != u.Email {
		return "expired", 0, nil
	}
	got := hashCode(code, u.UID)
	if !codeRe.MatchString(code) || subtle.ConstantTimeCompare([]byte(got), []byte(c.CodeHash)) != 1 {
		attempts, err := s.store.AddCodeAttempt(ctx, u.UID)
		if err != nil {
			return "", 0, err
		}
		return "wrong", max(0, maxCodeAttempts-attempts), nil
	}
	if err := s.store.SetEmailVerified(ctx, u.UID); err != nil {
		return "", 0, err
	}
	if err := s.store.DeleteEmailCode(ctx, u.UID); err != nil {
		s.log.Error("delete email code", "err", err)
	}
	return "verified", 0, nil
}

func (s *Server) checkVerifyCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, left, err := s.verifyCode(r.Context(), *userFrom(r.Context()), req.Code)
	switch {
	case err != nil:
		s.serverError(w, "verify code", err)
	case result == "verified":
		writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
	case result == "wrong":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wrong", "attemptsLeft": left})
	default:
		writeError(w, http.StatusBadRequest, result)
	}
}

// verifyLink handles the "Verify my email" button in the code email. It needs
// no sign-in, so it works on whichever device opens the email: the code in
// the link is the proof, with the same expiry and attempt limits. It then
// sends the visitor back to the site.
func (s *Server) verifyLink(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uid, code := q.Get("u"), q.Get("c")
	result := "expired"
	if uid != "" && len(uid) <= 128 {
		u, err := s.store.GetUser(r.Context(), uid)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			s.log.Error("verify link: get user", "err", err)
			result = "error"
		default:
			if result, _, err = s.verifyCode(r.Context(), u, code); err != nil {
				s.log.Error("verify link", "err", err)
				result = "error"
			}
		}
	}
	http.Redirect(w, r, s.cfg.SiteURL+"/login?verify="+result, http.StatusSeeOther)
}

func (s *Server) verifyURL(uid, code string) string {
	return s.cfg.PublicURL + "/v1/verify/link?u=" + url.QueryEscape(uid) + "&c=" + code
}

func codeEmail(code, link, language string) mail.Message {
	t := struct{ subject, hi, intro, button, or, expires, sign string }{
		"Your Speak Up Fearless code: " + code,
		"Hi there,",
		"Tap the button to verify your email, or enter this code on the site:",
		"Verify my email",
		"Your code",
		"It expires in 10 minutes. If you didn't sign up, you can ignore this email.",
		"Speak Up Fearless",
	}
	dir := "ltr"
	if language == "ar" {
		t = struct{ subject, hi, intro, button, or, expires, sign string }{
			"رمز Speak Up Fearless الخاص بك: " + code,
			"مرحبًا،",
			"اضغط الزر لتأكيد بريدك الإلكتروني، أو أدخل هذا الرمز في الموقع:",
			"تأكيد بريدي الإلكتروني",
			"رمزك",
			"تنتهي صلاحية الرمز خلال 10 دقائق. إذا لم تقم بالتسجيل، يمكنك تجاهل هذه الرسالة.",
			"Speak Up Fearless",
		}
		dir = "rtl"
	}
	text := t.hi + "\n\n" + t.intro + "\n\n" + code + "\n\n" + t.button + ": " + link + "\n\n" + t.expires + "\n\n" + t.sign
	esc := html.EscapeString
	htmlBody := `<!doctype html><html dir="` + dir + `"><body style="margin:0;background:#F3E3C8;font-family:Inter,Segoe UI,Arial,sans-serif;color:#2B1D14">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:32px 16px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:480px;background:#FFFAF3;border-radius:20px;padding:32px">
<tr><td style="font-size:20px;font-weight:700;padding-bottom:16px">` + esc(t.sign) + `</td></tr>
<tr><td style="font-size:16px;line-height:1.5;padding-bottom:8px">` + esc(t.hi) + `</td></tr>
<tr><td style="font-size:16px;line-height:1.5;padding-bottom:24px">` + esc(t.intro) + `</td></tr>
<tr><td align="center" style="padding-bottom:24px"><a href="` + esc(link) + `" style="display:inline-block;background:#C2622D;color:#ffffff;text-decoration:none;font-weight:700;font-size:17px;padding:14px 32px;border-radius:999px">` + esc(t.button) + `</a></td></tr>
<tr><td align="center" style="font-size:13px;color:#7A6656;padding-bottom:6px">` + esc(t.or) + `</td></tr>
<tr><td align="center" style="font-size:34px;font-weight:700;letter-spacing:8px;padding-bottom:24px">` + code + `</td></tr>
<tr><td style="font-size:13px;line-height:1.5;color:#7A6656">` + esc(t.expires) + `</td></tr>
</table></td></tr></table></body></html>`
	return mail.Message{Subject: t.subject, Text: text, HTML: htmlBody}
}
