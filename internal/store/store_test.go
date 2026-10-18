package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// testStores returns Memory, plus Postgres when TEST_DATABASE_URL is set
// (point it at a throwaway database: the test writes rows).
func testStores(t *testing.T) map[string]Store {
	stores := map[string]Store{"memory": NewMemory()}
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		pg, err := OpenPostgres(context.Background(), url)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(pg.Close)
		stores["postgres"] = pg
	}
	return stores
}

func TestStoreContract(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			uid := fmt.Sprintf("test-%d", time.Now().UnixNano())

			u, err := st.UpsertUser(ctx, User{UID: uid, Email: "a@example.com", DisplayName: "From Google"})
			if err != nil || u.Language != "en" || u.DisplayName != "From Google" {
				t.Fatalf("create user: %v %+v", err, u)
			}
			name := "Chosen"
			if _, err := st.UpdateUser(ctx, uid, UserUpdate{DisplayName: &name}); err != nil {
				t.Fatal(err)
			}
			pic, none := "data:image/jpeg;base64,AAAA", ""
			if u, err := st.UpdateUser(ctx, uid, UserUpdate{Avatar: &pic}); err != nil || u.Avatar == nil || *u.Avatar != pic || u.DisplayName != "Chosen" {
				t.Fatalf("set avatar: %v %+v", err, u)
			}
			if u, _ := st.UpdateUser(ctx, uid, UserUpdate{}); u.Avatar == nil {
				t.Fatalf("empty update cleared the avatar")
			}
			if u, _ := st.UpdateUser(ctx, uid, UserUpdate{Avatar: &none}); u.Avatar != nil {
				t.Fatalf("clear avatar: %+v", u)
			}
			u, _ = st.UpsertUser(ctx, User{UID: uid, Email: "b@example.com", DisplayName: "From Google"})
			if u.DisplayName != "Chosen" || u.Email != "b@example.com" {
				t.Fatalf("re-sign-in: %+v", u)
			}
			if _, err := st.GetUser(ctx, "nobody-"+uid); err != ErrNotFound {
				t.Fatalf("missing user: %v", err)
			}

			three, five := 3, 5
			for _, s := range []GameSession{
				{UID: uid, GameID: "triple-step", DurationSeconds: 60, Score: &three, Details: []byte(`{"topic":"travel"}`)},
				{UID: uid, GameID: "triple-step", DurationSeconds: 30, Score: &five},
				{UID: uid, GameID: "podcast", DurationSeconds: 100},
			} {
				if _, err := st.AddGameSession(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			list, err := st.ListGameSessions(ctx, uid, "triple-step", 10)
			if err != nil || len(list) != 2 {
				t.Fatalf("list sessions: %v %d", err, len(list))
			}
			progress, err := st.GameProgress(ctx, uid)
			if err != nil || len(progress) != 2 {
				t.Fatalf("progress: %v %+v", err, progress)
			}
			for _, g := range progress {
				if g.GameID == "triple-step" && (g.Plays != 2 || g.TotalSeconds != 90 || *g.BestScore != 5) {
					t.Fatalf("triple-step progress: %+v", g)
				}
			}

			b, err := st.AddBooking(ctx, Booking{UID: &uid, Name: "Sam", Contact: "x", PreferredTime: "Mon", Reason: "Presentation at work", Course: "single"})
			if err != nil || b.Status != "new" || b.ID == 0 || b.Reason != "Presentation at work" || b.Course != "single" {
				t.Fatalf("add booking: %v %+v", err, b)
			}
			mine, _ := st.ListBookingsForUser(ctx, uid)
			if len(mine) != 1 {
				t.Fatalf("my bookings: %d", len(mine))
			}
			if b, err = st.SetBookingStatus(ctx, b.ID, "scheduled"); err != nil || b.Status != "scheduled" {
				t.Fatalf("set status: %v %+v", err, b)
			}
			if _, err := st.SetBookingStatus(ctx, -1, "closed"); err != ErrNotFound {
				t.Fatalf("missing booking: %v", err)
			}

			// Email verification codes.
			if u, _ := st.GetUser(ctx, uid); u.EmailVerified {
				t.Fatalf("new user should be unverified")
			}
			exp := time.Now().Add(10 * time.Minute)
			if err := st.PutEmailCode(ctx, EmailCode{UID: uid, Email: "b@example.com", CodeHash: "h1", ExpiresAt: exp}); err != nil {
				t.Fatal(err)
			}
			if n, err := st.AddCodeAttempt(ctx, uid); err != nil || n != 1 {
				t.Fatalf("attempt: %v %d", err, n)
			}
			if err := st.PutEmailCode(ctx, EmailCode{UID: uid, Email: "b@example.com", CodeHash: "h2", ExpiresAt: exp}); err != nil {
				t.Fatal(err)
			}
			_ = st.RecordCodeSend(ctx, uid)
			_ = st.RecordCodeSend(ctx, uid)
			c, err := st.GetEmailCode(ctx, uid)
			if err != nil || c.CodeHash != "h2" || c.Attempts != 0 || c.ExpiresAt.Unix() != exp.Unix() {
				t.Fatalf("replaced code: %v %+v", err, c)
			}
			if sends, err := st.CodeSendsSince(ctx, uid, time.Now().Add(-time.Hour)); err != nil || len(sends) != 2 {
				t.Fatalf("sends: %v %d", err, len(sends))
			}
			if err := st.DeleteEmailCode(ctx, uid); err != nil {
				t.Fatal(err)
			}
			if _, err := st.GetEmailCode(ctx, uid); err != ErrNotFound {
				t.Fatalf("deleted code: %v", err)
			}
			if err := st.SetEmailVerified(ctx, uid); err != nil {
				t.Fatal(err)
			}
			if u, _ := st.UpsertUser(ctx, User{UID: uid, Email: "b@example.com"}); !u.EmailVerified {
				t.Fatalf("verified flag lost on sign-in")
			}
			if u, _ := st.UpsertUser(ctx, User{UID: uid, Email: "new@example.com"}); u.EmailVerified {
				t.Fatalf("changing email should clear verification")
			}
		})
	}
}
