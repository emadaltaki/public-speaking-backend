package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emadaltaki/public-speaking-backend/internal/mail"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

// Personal emails (welcome, streak start, streak reminder) only go out when
// EmailMode allows it: "off" sends nothing, "test" sends only to
// EmailTestList, "on" sends to everyone who hasn't opted out.
func (s *Server) mayEmail(u store.User) bool {
	if s.cfg.Mailer == nil || s.cfg.CronSecret == "" || u.Email == "" || !u.EmailVerified {
		return false
	}
	switch s.cfg.EmailMode {
	case "on":
		return true
	case "test":
		for _, e := range s.cfg.EmailTestList {
			if strings.EqualFold(e, u.Email) {
				return true
			}
		}
	}
	return false
}

func (s *Server) unsubToken(uid string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.CronSecret))
	mac.Write([]byte("unsubscribe:" + uid))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (s *Server) unsubURL(uid string) string {
	return s.cfg.PublicURL + "/v1/email/unsubscribe?u=" + url.QueryEscape(uid) + "&t=" + s.unsubToken(uid)
}

func (s *Server) emailInfo(u store.User) userEmailInfo {
	return userEmailInfo{Name: u.DisplayName, Lang: u.Language, Unsub: s.unsubURL(u.UID)}
}

func (s *Server) location(u store.User) *time.Location {
	if u.TimeZone != "" {
		if loc, err := time.LoadLocation(u.TimeZone); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation(s.cfg.DefaultTimeZone); err == nil {
		return loc
	}
	return time.UTC
}

// dailyStreak counts consecutive local days with a daily round, ending today
// if the user already played today, else yesterday.
func dailyStreak(sessions []store.GameSession, loc *time.Location, now time.Time) (streak int, playedToday bool, todayRounds int) {
	days := map[string]bool{}
	today := now.In(loc).Format(time.DateOnly)
	for _, gs := range sessions {
		d := gs.PlayedAt.In(loc).Format(time.DateOnly)
		days[d] = true
		if d == today {
			todayRounds++
		}
	}
	playedToday = days[today]
	day := now.In(loc)
	if !playedToday {
		day = day.AddDate(0, 0, -1)
	}
	for days[day.Format(time.DateOnly)] {
		streak++
		day = day.AddDate(0, 0, -1)
	}
	return streak, playedToday, todayRounds
}

// sendOnce sends a personal email of this kind at most once per localDate.
func (s *Server) sendOnce(ctx context.Context, u store.User, kind, localDate string, m mail.Message) bool {
	fresh, err := s.store.MarkStreakEmail(ctx, u.UID, kind, localDate)
	if err != nil {
		s.log.Error("mark email", "kind", kind, "err", err)
		return false
	}
	if !fresh {
		return false
	}
	m.To = u.Email
	sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := s.cfg.Mailer.Send(sendCtx, m); err != nil {
		s.log.Error("send email", "kind", kind, "err", err)
		_ = s.store.UnmarkStreakEmail(ctx, u.UID, kind, localDate)
		return false
	}
	return true
}

// welcomeKey dates the one-off welcome email so it is only ever sent once.
const welcomeKey = "2000-01-01"

// maybeWelcome thanks someone for joining, once, after their email is verified.
func (s *Server) maybeWelcome(ctx context.Context, u store.User) {
	u.EmailVerified = true
	if !s.mayEmail(u) {
		return
	}
	s.sendOnce(ctx, u, "welcome", welcomeKey, s.welcomeEmail(s.emailInfo(u)))
}

// afterDailyRound sends the "you started a streak" email after the first
// daily round of a new streak.
func (s *Server) afterDailyRound(ctx context.Context, u store.User) {
	if !u.StreakEmails || !s.mayEmail(u) {
		return
	}
	sessions, err := s.store.ListGameSessions(ctx, u.UID, "daily", 400)
	if err != nil {
		s.log.Error("streak: list sessions", "err", err)
		return
	}
	loc := s.location(u)
	now := time.Now()
	streak, _, todayRounds := dailyStreak(sessions, loc, now)
	if streak != 1 || todayRounds != 1 {
		return
	}
	s.sendOnce(ctx, u, "start", now.In(loc).Format(time.DateOnly), s.streakStartEmail(s.emailInfo(u)))
}

// reminderHour is the local hour from which the evening reminder goes out.
// Any run between then and midnight sends it (once), so a late or skipped
// scheduler run still catches people.
const reminderHour = 19

// cronStreakReminders is called hourly by the scheduler with
// "Authorization: Bearer <CRON_SECRET>".
func (s *Server) cronStreakReminders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CronSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "CRON_SECRET not configured")
		return
	}
	want := "Bearer " + s.cfg.CronSecret
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		writeError(w, http.StatusUnauthorized, "bad cron secret")
		return
	}
	start := time.Now()
	now := start
	if at := r.URL.Query().Get("now"); at != "" && s.cfg.EmailMode != "on" {
		// Lets a test run pretend it's a given time; ignored once emails are live.
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			now = t
		}
	}
	users, err := s.store.StreakReminderCandidates(r.Context(), now.Add(-50*time.Hour))
	if err != nil {
		s.serverError(w, "streak candidates", err)
		return
	}
	res := map[string]any{"mode": s.cfg.EmailMode, "checked": len(users), "sent": 0, "notYet": 0, "playedToday": 0, "blocked": 0, "partial": false}
	sent := 0
	for _, u := range users {
		if time.Since(start) > 8*time.Second {
			res["partial"] = true // the next run picks up the rest
			break
		}
		loc := s.location(u)
		local := now.In(loc)
		if local.Hour() < reminderHour {
			res["notYet"] = res["notYet"].(int) + 1
			continue
		}
		sessions, err := s.store.ListGameSessions(r.Context(), u.UID, "daily", 400)
		if err != nil {
			s.log.Error("streak: list sessions", "err", err)
			continue
		}
		streak, playedToday, _ := dailyStreak(sessions, loc, now)
		if playedToday || streak < 1 {
			res["playedToday"] = res["playedToday"].(int) + 1
			continue
		}
		if !s.mayEmail(u) {
			res["blocked"] = res["blocked"].(int) + 1
			continue
		}
		if s.sendOnce(r.Context(), u, "reminder", local.Format(time.DateOnly), s.streakReminderEmail(s.emailInfo(u), streak)) {
			sent++
		}
	}
	res["sent"] = sent
	writeJSON(w, http.StatusOK, res)
}

// unsubscribe turns off streak and welcome emails from the link in an email
// (GET from a click, POST from a mail app's one-click unsubscribe).
func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	uid, token := r.URL.Query().Get("u"), r.URL.Query().Get("t")
	ok := s.cfg.CronSecret != "" && uid != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(s.unsubToken(uid))) == 1
	if ok {
		off := false
		if _, err := s.store.UpdateUser(r.Context(), uid, store.UserUpdate{StreakEmails: &off}); err != nil {
			s.log.Error("unsubscribe", "err", err)
			ok = false
		}
	}
	if r.Method == http.MethodPost {
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid unsubscribe link")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"unsubscribed": true})
		return
	}
	target := s.cfg.SiteURL + "/account?emails=off"
	if !ok {
		target = s.cfg.SiteURL + "/account?emails=invalid"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
