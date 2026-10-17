package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"time"

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
	subject, body := codeEmail(code, u.Language)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cfg.Mailer.Send(ctx, u.Email, subject, body); err != nil {
		s.log.Error("send code email", "err", err)
		writeError(w, http.StatusBadGateway, "could not send email")
		return
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

func (s *Server) checkVerifyCode(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if u.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
		return
	}
	c, err := s.store.GetEmailCode(r.Context(), u.UID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "expired")
		return
	}
	if err != nil {
		s.serverError(w, "get email code", err)
		return
	}
	if time.Now().After(c.ExpiresAt) || c.Attempts >= maxCodeAttempts || c.Email != u.Email {
		writeError(w, http.StatusBadRequest, "expired")
		return
	}
	got := hashCode(req.Code, u.UID)
	if !codeRe.MatchString(req.Code) || subtle.ConstantTimeCompare([]byte(got), []byte(c.CodeHash)) != 1 {
		attempts, err := s.store.AddCodeAttempt(r.Context(), u.UID)
		if err != nil {
			s.serverError(w, "add code attempt", err)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":        "wrong",
			"attemptsLeft": max(0, maxCodeAttempts-attempts),
		})
		return
	}
	if err := s.store.SetEmailVerified(r.Context(), u.UID); err != nil {
		s.serverError(w, "set email verified", err)
		return
	}
	if err := s.store.DeleteEmailCode(r.Context(), u.UID); err != nil {
		s.log.Error("delete email code", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
}

func codeEmail(code, language string) (subject, body string) {
	if language == "ar" {
		return "رمز Speak Up Fearless الخاص بك: " + code,
			"مرحبًا،\n\n" +
				"رمز التحقق الخاص بك هو: " + code + "\n\n" +
				"تنتهي صلاحية الرمز خلال 10 دقائق. إذا لم تقم بالتسجيل، يمكنك تجاهل هذه الرسالة.\n\n" +
				"Speak Up Fearless"
	}
	return "Your Speak Up Fearless code: " + code,
		"Hi there,\n\n" +
			"Your verification code is: " + code + "\n\n" +
			"It expires in 10 minutes. If you didn't sign up, you can ignore this email.\n\n" +
			"Speak Up Fearless"
}
