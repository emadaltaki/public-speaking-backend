package store

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Store for tests and for running the API locally
// without a database. Data is lost on restart.
type Memory struct {
	mu       sync.Mutex
	users    map[string]User
	sessions []GameSession
	codes    map[string]EmailCode
	sends    map[string][]time.Time
	bookings []Booking
	nextID   int64
}

func NewMemory() *Memory {
	return &Memory{users: map[string]User{}, codes: map[string]EmailCode{}, sends: map[string][]time.Time{}}
}

func (m *Memory) id() int64 { m.nextID++; return m.nextID }

func (m *Memory) Ping(context.Context) error { return nil }
func (m *Memory) Close()                     {}

func (m *Memory) UpsertUser(_ context.Context, u User) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	existing, ok := m.users[u.UID]
	if !ok {
		u.Language = "en"
		u.CreatedAt, u.LastSeenAt = now, now
		m.users[u.UID] = u
		return u, nil
	}
	if existing.Email != u.Email {
		existing.EmailVerified = false
	}
	existing.Email = u.Email
	if u.PhotoURL != "" {
		existing.PhotoURL = u.PhotoURL
	}
	if existing.DisplayName == "" {
		existing.DisplayName = u.DisplayName
	}
	existing.LastSeenAt = now
	m.users[u.UID] = existing
	return existing, nil
}

func (m *Memory) GetUser(_ context.Context, uid string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[uid]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (m *Memory) UpdateUser(_ context.Context, uid string, upd UserUpdate) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[uid]
	if !ok {
		return User{}, ErrNotFound
	}
	if upd.DisplayName != nil {
		u.DisplayName = *upd.DisplayName
	}
	if upd.Language != nil {
		u.Language = *upd.Language
	}
	if upd.Avatar != nil {
		if *upd.Avatar == "" {
			u.Avatar = nil
		} else {
			avatar := *upd.Avatar
			u.Avatar = &avatar
		}
	}
	m.users[uid] = u
	return u, nil
}

func (m *Memory) AddGameSession(_ context.Context, s GameSession) (GameSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.ID = m.id()
	s.PlayedAt = time.Now().UTC()
	m.sessions = append(m.sessions, s)
	return s, nil
}

func (m *Memory) ListGameSessions(_ context.Context, uid, gameID string, limit int) ([]GameSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []GameSession{}
	for i := len(m.sessions) - 1; i >= 0 && len(out) < limit; i-- {
		s := m.sessions[i]
		if s.UID == uid && (gameID == "" || s.GameID == gameID) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *Memory) GameProgress(_ context.Context, uid string) ([]GameProgress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byGame := map[string]*GameProgress{}
	for _, s := range m.sessions {
		if s.UID != uid {
			continue
		}
		g, ok := byGame[s.GameID]
		if !ok {
			g = &GameProgress{GameID: s.GameID}
			byGame[s.GameID] = g
		}
		g.Plays++
		g.TotalSeconds += s.DurationSeconds
		if s.Score != nil && (g.BestScore == nil || *s.Score > *g.BestScore) {
			score := *s.Score
			g.BestScore = &score
		}
		if !s.PlayedAt.Before(g.LastPlayedAt) {
			g.LastPlayedAt = s.PlayedAt
		}
	}
	out := []GameProgress{}
	for _, g := range byGame {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastPlayedAt.After(out[j].LastPlayedAt) })
	return out, nil
}

func (m *Memory) AddBooking(_ context.Context, b Booking) (Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b.ID = m.id()
	b.Status = "new"
	b.CreatedAt = time.Now().UTC()
	m.bookings = append(m.bookings, b)
	return b, nil
}

func (m *Memory) ListBookingsForUser(_ context.Context, uid string) ([]Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Booking{}
	for i := len(m.bookings) - 1; i >= 0; i-- {
		if b := m.bookings[i]; b.UID != nil && *b.UID == uid {
			out = append(out, b)
		}
	}
	return out, nil
}

func (m *Memory) ListBookings(_ context.Context, limit int) ([]Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Booking{}
	for i := len(m.bookings) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.bookings[i])
	}
	return out, nil
}

func (m *Memory) SetBookingStatus(_ context.Context, id int64, status string) (Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.bookings {
		if m.bookings[i].ID == id {
			m.bookings[i].Status = status
			return m.bookings[i], nil
		}
	}
	return Booking{}, ErrNotFound
}

func (m *Memory) SetEmailVerified(_ context.Context, uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[uid]
	if !ok {
		return ErrNotFound
	}
	u.EmailVerified = true
	m.users[uid] = u
	return nil
}

func (m *Memory) PutEmailCode(_ context.Context, c EmailCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.Attempts = 0
	m.codes[c.UID] = c
	m.sends[c.UID] = append(m.sends[c.UID], time.Now())
	return nil
}

func (m *Memory) GetEmailCode(_ context.Context, uid string) (EmailCode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[uid]
	if !ok {
		return EmailCode{}, ErrNotFound
	}
	return c, nil
}

func (m *Memory) AddCodeAttempt(_ context.Context, uid string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[uid]
	if !ok {
		return 0, ErrNotFound
	}
	c.Attempts++
	m.codes[uid] = c
	return c.Attempts, nil
}

func (m *Memory) DeleteEmailCode(_ context.Context, uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.codes, uid)
	return nil
}

func (m *Memory) CodeSendsSince(_ context.Context, uid string, t time.Time) ([]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []time.Time
	for i := len(m.sends[uid]) - 1; i >= 0; i-- {
		if at := m.sends[uid][i]; at.After(t) {
			out = append(out, at)
		}
	}
	return out, nil
}

// ShiftSends moves a user's recorded code sends by d. Tests use it to
// simulate sends that happened earlier.
func (m *Memory) ShiftSends(uid string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sends[uid] {
		m.sends[uid][i] = m.sends[uid][i].Add(d)
	}
}
