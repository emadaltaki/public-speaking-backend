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
	UID         string    `json:"uid"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	PhotoURL    string    `json:"photoUrl"`
	Language    string    `json:"language"` // "en" or "ar"
	CreatedAt   time.Time `json:"createdAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
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
	UpdateUser(ctx context.Context, uid string, displayName, language *string) (User, error)

	AddGameSession(ctx context.Context, s GameSession) (GameSession, error)
	ListGameSessions(ctx context.Context, uid, gameID string, limit int) ([]GameSession, error)
	GameProgress(ctx context.Context, uid string) ([]GameProgress, error)

	AddBooking(ctx context.Context, b Booking) (Booking, error)
	ListBookingsForUser(ctx context.Context, uid string) ([]Booking, error)
	ListBookings(ctx context.Context, limit int) ([]Booking, error)
	SetBookingStatus(ctx context.Context, id int64, status string) (Booking, error)

	Ping(ctx context.Context) error
	Close()
}
