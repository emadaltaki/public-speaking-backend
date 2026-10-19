// Package api is the HTTP layer: routing, CORS, auth middleware and handlers.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/emadaltaki/public-speaking-backend/internal/auth"
	"github.com/emadaltaki/public-speaking-backend/internal/mail"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

type Config struct {
	// AllowedOrigins are exact origins allowed by CORS, e.g.
	// "https://public-speaking-lemon.vercel.app". Any http://localhost:<port>
	// origin is also allowed when AllowLocalhost is true.
	AllowedOrigins []string
	AllowLocalhost bool
	// AdminEmails may list and update all booking requests. Their Firebase
	// email must be verified.
	AdminEmails []string
	// Mailer sends verification codes. Nil when SMTP isn't configured.
	Mailer mail.Sender
	// SiteURL is where the "Verify my email" link sends people afterwards,
	// and PublicURL is this API's own address, used to build that link.
	SiteURL   string
	PublicURL string
	// CronSecret authenticates the scheduled reminder run and signs
	// unsubscribe links. Personal emails stay off without it.
	CronSecret string
	// EmailMode gates welcome and streak emails: "off", "test" (only
	// EmailTestList) or "on".
	EmailMode     string
	EmailTestList []string
	// DefaultTimeZone is used until the site sends a user's own time zone.
	DefaultTimeZone string
}

type Server struct {
	cfg      Config
	store    store.Store
	verifier auth.Verifier
	limiter  *rateLimiter
	log      *slog.Logger
}

func NewServer(cfg Config, st store.Store, v auth.Verifier, log *slog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		store:    st,
		verifier: v,
		limiter:  newRateLimiter(5, 10*time.Minute),
		log:      log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.root)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent) // browsers ask for this when the API URL is opened
	})

	mux.Handle("GET /v1/me", s.requireUser(s.getMe))
	mux.Handle("PATCH /v1/me", s.requireUser(s.patchMe))
	mux.Handle("POST /v1/me/verify/send", s.requireUser(s.sendVerifyCode))
	mux.Handle("POST /v1/me/verify/check", s.requireUser(s.checkVerifyCode))
	mux.HandleFunc("GET /v1/verify/link", s.verifyLink)
	mux.HandleFunc("GET /v1/cron/streak-reminders", s.cronStreakReminders)
	mux.HandleFunc("POST /v1/cron/streak-reminders", s.cronStreakReminders)
	mux.HandleFunc("GET /v1/email/unsubscribe", s.unsubscribe)
	mux.HandleFunc("POST /v1/email/unsubscribe", s.unsubscribe)

	mux.Handle("POST /v1/games/sessions", s.requireUser(s.addGameSession))
	mux.Handle("GET /v1/games/sessions", s.requireUser(s.listGameSessions))
	mux.Handle("GET /v1/games/progress", s.requireUser(s.gameProgress))

	// Booking requests come from visitors who may not be signed in.
	mux.Handle("POST /v1/bookings", s.optionalUser(s.addBooking))
	mux.Handle("GET /v1/bookings", s.requireUser(s.listMyBookings))

	mux.Handle("GET /v1/admin/bookings", s.requireAdmin(s.adminListBookings))
	mux.Handle("PATCH /v1/admin/bookings/{id}", s.requireAdmin(s.adminSetBookingStatus))

	return s.recoverer(s.logRequests(s.cors(limitBody(mux, 300<<10))))
}

type ctxKey struct{}

// userFrom returns the signed-in user, or nil on optional-auth routes.
func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(ctxKey{}).(*store.User)
	return u
}

// authenticate verifies the bearer token and makes sure the user row exists.
// It returns (nil, nil) when no token was sent.
func (s *Server) authenticate(r *http.Request) (*store.User, *auth.User, error) {
	token := auth.BearerToken(r)
	if token == "" {
		return nil, nil, nil
	}
	claims, err := s.verifier.Verify(r.Context(), token)
	if err != nil {
		return nil, nil, err
	}
	u, err := s.store.UpsertUser(r.Context(), store.User{
		UID:         claims.UID,
		Email:       claims.Email,
		DisplayName: claims.Name,
		PhotoURL:    claims.Picture,
	})
	if err != nil {
		return nil, nil, err
	}
	// Firebase already verified this email (e.g. Google sign-in): record it.
	if claims.EmailVerified && claims.Email != "" && !u.EmailVerified {
		if err := s.store.SetEmailVerified(r.Context(), u.UID); err != nil {
			return nil, nil, err
		}
		u.EmailVerified = true
		s.maybeWelcome(r.Context(), u)
	}
	return &u, claims, nil
}

func (s *Server) withAuth(required bool, admin bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, claims, err := s.authenticate(r)
		switch {
		case errors.Is(err, auth.ErrInvalidToken):
			writeError(w, http.StatusUnauthorized, "invalid or expired sign-in token")
			return
		case err != nil:
			s.log.Error("authenticate", "err", err)
			writeError(w, http.StatusInternalServerError, "could not verify sign-in")
			return
		case u == nil && required:
			writeError(w, http.StatusUnauthorized, "sign-in required")
			return
		}
		if admin && !s.isAdmin(claims) {
			writeError(w, http.StatusForbidden, "admin only")
			return
		}
		if u != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
		}
		next(w, r)
	})
}

func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return s.withAuth(true, false, next)
}
func (s *Server) optionalUser(next http.HandlerFunc) http.Handler {
	return s.withAuth(false, false, next)
}
func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return s.withAuth(true, true, next)
}

func (s *Server) isAdmin(c *auth.User) bool {
	if c == nil || !c.EmailVerified || c.Email == "" {
		return false
	}
	for _, e := range s.cfg.AdminEmails {
		if strings.EqualFold(e, c.Email) {
			return true
		}
	}
	return false
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "3600")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	for _, o := range s.cfg.AllowedOrigins {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	if s.cfg.AllowLocalhost {
		for _, prefix := range []string{"http://localhost:", "http://127.0.0.1:"} {
			if strings.HasPrefix(origin, prefix) {
				return true
			}
		}
		return origin == "http://localhost" || origin == "http://127.0.0.1"
	}
	return false
}

func limitBody(next http.Handler, n int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/favicon.ico" {
			return
		}
		s.log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "value", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
