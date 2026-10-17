package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status, code := "ok", http.StatusOK
	if err := s.store.Ping(r.Context()); err != nil {
		s.log.Error("health: database ping", "err", err)
		status, code = "database unavailable", http.StatusServiceUnavailable
	}
	database := "postgres"
	if _, ok := s.store.(*store.Memory); ok {
		database = "memory (data is lost on restart: set DATABASE_URL)"
	}
	writeJSON(w, code, map[string]string{"status": status, "database": database})
}

// root answers visits to the bare API URL so a browser shows something useful.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "Speak Up Fearless API",
		"health":  "/healthz",
	})
}

// --- users ---

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFrom(r.Context()))
}

func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName *string `json:"displayName"`
		Language    *string `json:"language"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if utf8.RuneCountInString(name) > 80 {
			writeError(w, http.StatusBadRequest, "displayName must be at most 80 characters")
			return
		}
		req.DisplayName = &name
	}
	if req.Language != nil && *req.Language != "en" && *req.Language != "ar" {
		writeError(w, http.StatusBadRequest, `language must be "en" or "ar"`)
		return
	}
	u, err := s.store.UpdateUser(r.Context(), userFrom(r.Context()).UID, req.DisplayName, req.Language)
	if err != nil {
		s.serverError(w, "update user", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// --- games ---

var gameIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (s *Server) addGameSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GameID          string          `json:"gameId"`
		DurationSeconds int             `json:"durationSeconds"`
		Score           *int            `json:"score"`
		Details         json.RawMessage `json:"details"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	switch {
	case !gameIDRe.MatchString(req.GameID):
		writeError(w, http.StatusBadRequest, "gameId must be a lowercase slug like \"triple-step\"")
		return
	case req.DurationSeconds < 0 || req.DurationSeconds > 6*60*60:
		writeError(w, http.StatusBadRequest, "durationSeconds must be between 0 and 21600")
		return
	case len(req.Details) > 8<<10:
		writeError(w, http.StatusBadRequest, "details must be at most 8 KB")
		return
	}
	if len(req.Details) > 0 && string(req.Details) == "null" {
		req.Details = nil
	}
	if len(req.Details) > 0 && req.Details[0] != '{' {
		writeError(w, http.StatusBadRequest, "details must be a JSON object")
		return
	}
	gs, err := s.store.AddGameSession(r.Context(), store.GameSession{
		UID:             userFrom(r.Context()).UID,
		GameID:          req.GameID,
		DurationSeconds: req.DurationSeconds,
		Score:           req.Score,
		Details:         req.Details,
	})
	if err != nil {
		s.serverError(w, "add game session", err)
		return
	}
	writeJSON(w, http.StatusCreated, gs)
}

func (s *Server) listGameSessions(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("gameId")
	if gameID != "" && !gameIDRe.MatchString(gameID) {
		writeError(w, http.StatusBadRequest, "invalid gameId")
		return
	}
	limit := queryInt(r, "limit", 50, 1, 200)
	list, err := s.store.ListGameSessions(r.Context(), userFrom(r.Context()).UID, gameID, limit)
	if err != nil {
		s.serverError(w, "list game sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) gameProgress(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.GameProgress(r.Context(), userFrom(r.Context()).UID)
	if err != nil {
		s.serverError(w, "game progress", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": list})
}

// --- bookings ---

func (s *Server) addBooking(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many booking requests, please try again later")
		return
	}
	var req struct {
		Name          string `json:"name"`
		Contact       string `json:"contact"`
		PreferredTime string `json:"preferredTime"`
		Reason        string `json:"reason"`
		Course        string `json:"requestedCourse"`
		Description   string `json:"description"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Contact = strings.TrimSpace(req.Contact)
	req.PreferredTime = strings.TrimSpace(req.PreferredTime)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Course = strings.TrimSpace(req.Course)
	req.Description = strings.TrimSpace(req.Description)
	for _, f := range []struct {
		name, value string
		max         int
		required    bool
	}{
		{"name", req.Name, 100, true},
		{"contact", req.Contact, 200, true},
		{"preferredTime", req.PreferredTime, 200, true},
		{"reason", req.Reason, 200, true},
		{"description", req.Description, 2000, false},
	} {
		if f.required && f.value == "" {
			writeError(w, http.StatusBadRequest, f.name+" is required")
			return
		}
		if utf8.RuneCountInString(f.value) > f.max {
			writeError(w, http.StatusBadRequest, f.name+" must be at most "+strconv.Itoa(f.max)+" characters")
			return
		}
	}
	// The site sends a short slug such as "intro", "single", "four" or "eight".
	if req.Course != "" && !gameIDRe.MatchString(req.Course) {
		writeError(w, http.StatusBadRequest, "requestedCourse must be a lowercase slug like \"four\"")
		return
	}
	b := store.Booking{
		Name:          req.Name,
		Contact:       req.Contact,
		PreferredTime: req.PreferredTime,
		Reason:        req.Reason,
		Course:        req.Course,
		Description:   req.Description,
	}
	if u := userFrom(r.Context()); u != nil {
		b.UID = &u.UID
	}
	b, err := s.store.AddBooking(r.Context(), b)
	if err != nil {
		s.serverError(w, "add booking", err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) listMyBookings(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListBookingsForUser(r.Context(), userFrom(r.Context()).UID)
	if err != nil {
		s.serverError(w, "list bookings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookings": list})
}

func (s *Server) adminListBookings(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListBookings(r.Context(), queryInt(r, "limit", 100, 1, 500))
	if err != nil {
		s.serverError(w, "admin list bookings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookings": list})
}

var bookingStatuses = map[string]bool{"new": true, "contacted": true, "scheduled": true, "closed": true}

func (s *Server) adminSetBookingStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid booking id")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !bookingStatuses[req.Status] {
		writeError(w, http.StatusBadRequest, "status must be new, contacted, scheduled or closed")
		return
	}
	b, err := s.store.SetBookingStatus(r.Context(), id, req.Status)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "booking not found")
		return
	}
	if err != nil {
		s.serverError(w, "set booking status", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// --- helpers ---

func (s *Server) serverError(w http.ResponseWriter, op string, err error) {
	s.log.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func queryInt(r *http.Request, key string, def, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return max(lo, min(n, hi))
}

// clientIP uses the first X-Forwarded-For hop, which Cloud Run and most hosts
// set to the real client address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter allows n events per window per key. It is per-instance, which
// is enough to stop a form being spammed from one address.
type rateLimiter struct {
	n      int
	window time.Duration
	mu     sync.Mutex
	hits   map[string][]time.Time
}

func newRateLimiter(n int, window time.Duration) *rateLimiter {
	return &rateLimiter{n: n, window: window, hits: map[string][]time.Time{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	if len(l.hits) > 10000 { // drop stale keys so memory stays bounded
		for k, ts := range l.hits {
			if len(ts) == 0 || ts[len(ts)-1].Before(cutoff) {
				delete(l.hits, k)
			}
		}
	}
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.n {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}
