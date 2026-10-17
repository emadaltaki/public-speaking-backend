package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects and applies any migrations not yet run.
func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	p := &Postgres{pool: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return p, nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := p.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *Postgres) Close()                         { p.pool.Close() }

const userCols = `uid, email, display_name, photo_url, language, created_at, last_seen_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.UID, &u.Email, &u.DisplayName, &u.PhotoURL, &u.Language, &u.CreatedAt, &u.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// UpsertUser creates the user on first sign-in. On later sign-ins it refreshes
// email and photo from the token but keeps a display name the user chose.
func (p *Postgres) UpsertUser(ctx context.Context, u User) (User, error) {
	return scanUser(p.pool.QueryRow(ctx, `
		INSERT INTO users (uid, email, display_name, photo_url)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (uid) DO UPDATE SET
			email = EXCLUDED.email,
			photo_url = CASE WHEN EXCLUDED.photo_url <> '' THEN EXCLUDED.photo_url ELSE users.photo_url END,
			display_name = CASE WHEN users.display_name = '' THEN EXCLUDED.display_name ELSE users.display_name END,
			last_seen_at = now()
		RETURNING `+userCols, u.UID, u.Email, u.DisplayName, u.PhotoURL))
}

func (p *Postgres) GetUser(ctx context.Context, uid string) (User, error) {
	return scanUser(p.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE uid = $1`, uid))
}

func (p *Postgres) UpdateUser(ctx context.Context, uid string, displayName, language *string) (User, error) {
	return scanUser(p.pool.QueryRow(ctx, `
		UPDATE users SET
			display_name = COALESCE($2, display_name),
			language = COALESCE($3, language)
		WHERE uid = $1
		RETURNING `+userCols, uid, displayName, language))
}

func (p *Postgres) AddGameSession(ctx context.Context, s GameSession) (GameSession, error) {
	var details []byte
	if len(s.Details) > 0 {
		details = s.Details
	}
	err := p.pool.QueryRow(ctx, `
		INSERT INTO game_sessions (uid, game_id, duration_seconds, score, details)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, played_at`, s.UID, s.GameID, s.DurationSeconds, s.Score, details).Scan(&s.ID, &s.PlayedAt)
	return s, err
}

func (p *Postgres) ListGameSessions(ctx context.Context, uid, gameID string, limit int) ([]GameSession, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, uid, game_id, duration_seconds, score, details, played_at
		FROM game_sessions
		WHERE uid = $1 AND ($2 = '' OR game_id = $2)
		ORDER BY played_at DESC, id DESC
		LIMIT $3`, uid, gameID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GameSession{}
	for rows.Next() {
		var s GameSession
		var details []byte
		if err := rows.Scan(&s.ID, &s.UID, &s.GameID, &s.DurationSeconds, &s.Score, &details, &s.PlayedAt); err != nil {
			return nil, err
		}
		s.Details = details
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) GameProgress(ctx context.Context, uid string) ([]GameProgress, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT game_id, count(*), COALESCE(sum(duration_seconds), 0), max(score), max(played_at)
		FROM game_sessions WHERE uid = $1
		GROUP BY game_id ORDER BY max(played_at) DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GameProgress{}
	for rows.Next() {
		var g GameProgress
		if err := rows.Scan(&g.GameID, &g.Plays, &g.TotalSeconds, &g.BestScore, &g.LastPlayedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const bookingCols = `id, uid, name, contact, preferred_time, reason, requested_course, description, status, created_at`

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.UID, &b.Name, &b.Contact, &b.PreferredTime, &b.Reason, &b.Course, &b.Description, &b.Status, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func scanBookings(rows pgx.Rows) ([]Booking, error) {
	defer rows.Close()
	out := []Booking{}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (p *Postgres) AddBooking(ctx context.Context, b Booking) (Booking, error) {
	return scanBooking(p.pool.QueryRow(ctx, `
		INSERT INTO bookings (uid, name, contact, preferred_time, reason, requested_course, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+bookingCols, b.UID, b.Name, b.Contact, b.PreferredTime, b.Reason, b.Course, b.Description))
}

func (p *Postgres) ListBookingsForUser(ctx context.Context, uid string) ([]Booking, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+bookingCols+` FROM bookings WHERE uid = $1 ORDER BY created_at DESC`, uid)
	if err != nil {
		return nil, err
	}
	return scanBookings(rows)
}

func (p *Postgres) ListBookings(ctx context.Context, limit int) ([]Booking, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+bookingCols+` FROM bookings ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return scanBookings(rows)
}

func (p *Postgres) SetBookingStatus(ctx context.Context, id int64, status string) (Booking, error) {
	return scanBooking(p.pool.QueryRow(ctx,
		`UPDATE bookings SET status = $2 WHERE id = $1 RETURNING `+bookingCols, id, status))
}
