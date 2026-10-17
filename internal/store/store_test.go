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
			if _, err := st.UpdateUser(ctx, uid, &name, nil); err != nil {
				t.Fatal(err)
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

			b, err := st.AddBooking(ctx, Booking{UID: &uid, Name: "Sam", Contact: "x", PreferredTime: "Mon", Reason: "Presentation at work"})
			if err != nil || b.Status != "new" || b.ID == 0 || b.Reason != "Presentation at work" {
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
		})
	}
}
