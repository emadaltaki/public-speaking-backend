// Package store holds the API's data: users, game sessions and booking requests.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type User struct {
	UID           string    `json:"uid"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"displayName"`
	PhotoURL      string    `json:"photoUrl"`
	Language      string    `json:"language"` // "en" or "ar"
	Avatar        *string   `json:"avatar"`   // profile picture as a data URL, null when unset
	EmailVerified bool      `json:"emailVerified"`
	TimeZone      string    `json:"timeZone"`     // IANA name such as "America/Toronto"; empty until the site sends it
	StreakEmails  bool      `json:"streakEmails"` // streak start and reminder emails
	CreatedAt     time.Time `json:"createdAt"`
	LastSeenAt    time.Time `json:"lastSeenAt"`
}

// UserUpdate holds profile changes; nil fields are left as they are. An empty
// Avatar clears the picture.
type UserUpdate struct {
	DisplayName  *string
	Language     *string
	Avatar       *string
	TimeZone     *string
	StreakEmails *bool
}

// EmailCode is the pending verification code for one user. Only a hash of the
// code is stored.
type EmailCode struct {
	UID       string
	Email     string
	CodeHash  string
	ExpiresAt time.Time
	Attempts  int
}

// GameSession is one finished round of a practice game.
type GameSession struct {
	ID              int64           `json:"id"`
	UID             string          `json:"-"`
	GameID          string          `json:"gameId"`
	DurationSeconds int             `json:"durationSeconds"`
	Score           *int            `json:"score,omitempty"`
	Details         json.RawMessage `json:"details,omitempty"` // game-specific settings/results
	PlayedAt        time.Time       `json:"playedAt"`
}

// GameProgress sums a user's sessions for one game.
type GameProgress struct {
	GameID       string    `json:"gameId"`
	Plays        int       `json:"plays"`
	TotalSeconds int       `json:"totalSeconds"`
	BestScore    *int      `json:"bestScore,omitempty"`
	LastPlayedAt time.Time `json:"lastPlayedAt"`
}

type Booking struct {
	ID            int64     `json:"id"`
	UID           *string   `json:"uid,omitempty"` // set when the visitor was signed in
	Name          string    `json:"name"`
	Contact       string    `json:"contact"`
	PreferredTime string    `json:"preferredTime"`
	Reason        string    `json:"reason"`          // why they want the appointment
	Course        string    `json:"requestedCourse"` // session/package slug they picked, e.g. "four"; may be empty
	Description   string    `json:"description"`
	Status        string    `json:"status"` // new, contacted, scheduled, closed
	CreatedAt     time.Time `json:"createdAt"`
}

// Store is implemented by Postgres for real use and by Memory for tests and
// local runs without a database.
type Store interface {
	UpsertUser(ctx context.Context, u User) (User, error)
	GetUser(ctx context.Context, uid string) (User, error)
	UpdateUser(ctx context.Context, uid string, upd UserUpdate) (User, error)

	AddGameSession(ctx context.Context, s GameSession) (GameSession, error)
	ListGameSessions(ctx context.Context, uid, gameID string, limit int) ([]GameSession, error)
	GameProgress(ctx context.Context, uid string) ([]GameProgress, error)

	AddBooking(ctx context.Context, b Booking) (Booking, error)
	ListBookingsForUser(ctx context.Context, uid string) ([]Booking, error)
	ListBookings(ctx context.Context, limit int) ([]Booking, error)
	SetBookingStatus(ctx context.Context, id int64, status string) (Booking, error)

	SetEmailVerified(ctx context.Context, uid string) error
	// PutEmailCode replaces the user's pending code.
	PutEmailCode(ctx context.Context, c EmailCode) error
	// RecordCodeSend notes that a code email went out, for rate limits.
	RecordCodeSend(ctx context.Context, uid string) error
	GetEmailCode(ctx context.Context, uid string) (EmailCode, error)
	// AddCodeAttempt counts a wrong guess and returns the attempts so far.
	AddCodeAttempt(ctx context.Context, uid string) (int, error)
	DeleteEmailCode(ctx context.Context, uid string) error
	// CodeSendsSince returns when codes were sent to the user since t, newest first.
	CodeSendsSince(ctx context.Context, uid string, t time.Time) ([]time.Time, error)

	// MarkStreakEmail records that a streak email of this kind is going out
	// for the user's local day. It returns false if one already went out.
	MarkStreakEmail(ctx context.Context, uid, kind, localDate string) (bool, error)
	// UnmarkStreakEmail undoes MarkStreakEmail when the email failed to send.
	UnmarkStreakEmail(ctx context.Context, uid, kind, localDate string) error
	// StreakReminderCandidates returns users with streak emails on and a
	// verified email who played the daily round since t.
	StreakReminderCandidates(ctx context.Context, since time.Time) ([]User, error)

	Ping(ctx context.Context) error
	Close()
}
