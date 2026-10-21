package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

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

const userCols = `uid, email, display_name, photo_url, language, avatar, email_verified, time_zone, streak_emails, created_at, last_seen_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.UID, &u.Email, &u.DisplayName, &u.PhotoURL, &u.Language, &u.Avatar, &u.EmailVerified, &u.TimeZone, &u.StreakEmails, &u.CreatedAt, &u.LastSeenAt)
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
			email_verified = users.email_verified AND users.email = EXCLUDED.email,
			email = EXCLUDED.email,
			photo_url = CASE WHEN EXCLUDED.photo_url <> '' THEN EXCLUDED.photo_url ELSE users.photo_url END,
			display_name = CASE WHEN users.display_name = '' THEN EXCLUDED.display_name ELSE users.display_name END,
			last_seen_at = now()
		RETURNING `+userCols, u.UID, u.Email, u.DisplayName, u.PhotoURL))
}

func (p *Postgres) GetUser(ctx context.Context, uid string) (User, error) {
	return scanUser(p.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE uid = $1`, uid))
}

func (p *Postgres) UpdateUser(ctx context.Context, uid string, upd UserUpdate) (User, error) {
	return scanUser(p.pool.QueryRow(ctx, `
		UPDATE users SET
			display_name = COALESCE($2, display_name),
			language = COALESCE($3, language),
			avatar = CASE WHEN $4::text IS NULL THEN avatar ELSE NULLIF($4, '') END,
			time_zone = COALESCE($5, time_zone),
			streak_emails = COALESCE($6, streak_emails)
		WHERE uid = $1
		RETURNING `+userCols, uid, upd.DisplayName, upd.Language, upd.Avatar, upd.TimeZone, upd.StreakEmails))
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

func (p *Postgres) SetEmailVerified(ctx context.Context, uid string) error {
	_, err := p.pool.Exec(ctx, `UPDATE users SET email_verified = true WHERE uid = $1`, uid)
	return err
}

func (p *Postgres) PutEmailCode(ctx context.Context, c EmailCode) error {
	_, err := p.pool.Exec(ctx, `
			INSERT INTO email_codes (uid, email, code_hash, expires_at, attempts)
			VALUES ($1, $2, $3, $4, 0)
			ON CONFLICT (uid) DO UPDATE SET
				email = EXCLUDED.email, code_hash = EXCLUDED.code_hash,
				expires_at = EXCLUDED.expires_at, attempts = 0, created_at = now()`,
		c.UID, c.Email, c.CodeHash, c.ExpiresAt)
	return err
}

func (p *Postgres) RecordCodeSend(ctx context.Context, uid string) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO email_code_sends (uid) VALUES ($1)`, uid)
	return err
}

func (p *Postgres) GetEmailCode(ctx context.Context, uid string) (EmailCode, error) {
	var c EmailCode
	err := p.pool.QueryRow(ctx, `
		SELECT uid, email, code_hash, expires_at, attempts FROM email_codes WHERE uid = $1`, uid).
		Scan(&c.UID, &c.Email, &c.CodeHash, &c.ExpiresAt, &c.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (p *Postgres) AddCodeAttempt(ctx context.Context, uid string) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx,
		`UPDATE email_codes SET attempts = attempts + 1 WHERE uid = $1 RETURNING attempts`, uid).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

func (p *Postgres) DeleteEmailCode(ctx context.Context, uid string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM email_codes WHERE uid = $1`, uid)
	return err
}

func (p *Postgres) CodeSendsSince(ctx context.Context, uid string, t time.Time) ([]time.Time, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT sent_at FROM email_code_sends WHERE uid = $1 AND sent_at > $2 ORDER BY sent_at DESC`, uid, t)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

func (p *Postgres) MarkStreakEmail(ctx context.Context, uid, kind, localDate string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO streak_email_log (uid, kind, local_date) VALUES ($1, $2, $3::date)
		ON CONFLICT DO NOTHING`, uid, kind, localDate)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (p *Postgres) UnmarkStreakEmail(ctx context.Context, uid, kind, localDate string) error {
	_, err := p.pool.Exec(ctx,
		`DELETE FROM streak_email_log WHERE uid = $1 AND kind = $2 AND local_date = $3::date`, uid, kind, localDate)
	return err
}

func (p *Postgres) StreakReminderCandidates(ctx context.Context, since time.Time) ([]User, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT `+userCols+` FROM users
		WHERE streak_emails AND email_verified AND email <> ''
		AND EXISTS (SELECT 1 FROM game_sessions g
		            WHERE g.uid = users.uid AND g.game_id = 'daily' AND g.played_at > $1)`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (p *Postgres) AddSpeechReport(ctx context.Context, uid string, report json.RawMessage) (SpeechReport, error) {
	r := SpeechReport{Report: report}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO speech_reports (uid, report) VALUES ($1, $2) RETURNING id, created_at`, uid, []byte(report)).
		Scan(&r.ID, &r.CreatedAt)
	return r, err
}

func (p *Postgres) ListSpeechReports(ctx context.Context, uid string, limit int) ([]SpeechReport, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, created_at, report - 'transcript' - 'moments' - 'metrics'
		FROM speech_reports WHERE uid = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, uid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpeechReport{}
	for rows.Next() {
		var r SpeechReport
		var body []byte
		if err := rows.Scan(&r.ID, &r.CreatedAt, &body); err != nil {
			return nil, err
		}
		r.Report = body
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) GetSpeechReport(ctx context.Context, uid string, id int64) (SpeechReport, error) {
	r := SpeechReport{ID: id}
	var body []byte
	err := p.pool.QueryRow(ctx,
		`SELECT created_at, report FROM speech_reports WHERE uid = $1 AND id = $2`, uid, id).Scan(&r.CreatedAt, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	r.Report = body
	return r, err
}

func (p *Postgres) CountSpeechReportsSince(ctx context.Context, uid string, since time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx,
		`SELECT count(*) FROM speech_reports WHERE uid = $1 AND created_at > $2`, uid, since).Scan(&n)
	return n, err
}

func (p *Postgres) AddSpeechRetry(ctx context.Context, uid string, reportID int64, moment, stars int) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO speech_retries (uid, report_id, moment, stars) VALUES ($1, $2, $3, $4)`, uid, reportID, moment, stars)
	return err
}

func (p *Postgres) CountSpeechRetriesSince(ctx context.Context, uid string, since time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx,
		`SELECT count(*) FROM speech_retries WHERE uid = $1 AND created_at > $2`, uid, since).Scan(&n)
	return n, err
}

func (p *Postgres) BestRetryStars(ctx context.Context, uid string, reportID int64) (map[int]int, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT moment, max(stars) FROM speech_retries
		WHERE uid = $1 AND report_id = $2 GROUP BY moment`, uid, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var m, s int
		if err := rows.Scan(&m, &s); err != nil {
			return nil, err
		}
		out[m] = s
	}
	return out, rows.Err()
}

func (p *Postgres) CountGuestAnalysesSince(ctx context.Context, guestID, ip string, since time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `
		SELECT count(*) FROM guest_analyses
		WHERE (guest_id = $1 OR ip = $2) AND created_at > $3`, guestID, ip, since).Scan(&n)
	return n, err
}

func (p *Postgres) AddGuestAnalysis(ctx context.Context, guestID, ip string) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx,
		`INSERT INTO guest_analyses (guest_id, ip) VALUES ($1, $2) RETURNING id`, guestID, ip).Scan(&id)
	return id, err
}

func (p *Postgres) DeleteGuestAnalysis(ctx context.Context, id int64) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM guest_analyses WHERE id = $1`, id)
	return err
}
