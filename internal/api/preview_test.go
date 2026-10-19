package api

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestWritePreviews writes each email as HTML for a visual check:
// EMAIL_PREVIEW_DIR=/some/dir go test ./internal/api -run Previews
func TestWritePreviews(t *testing.T) {
	dir := os.Getenv("EMAIL_PREVIEW_DIR")
	if dir == "" {
		t.Skip("EMAIL_PREVIEW_DIR not set")
	}
	s := NewServer(Config{SiteURL: "https://public-speaking-lemon.vercel.app", PublicURL: "https://public-speaking-backend.vercel.app"},
		nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	info := userEmailInfo{Name: "Sara Ahmed", Lang: "en", Unsub: "https://public-speaking-backend.vercel.app/v1/email/unsubscribe?u=x&t=y"}
	ar := info
	ar.Lang, ar.Name = "ar", "سارة"
	for name, html := range map[string]string{
		"1-code":         s.codeEmail("482915", "https://public-speaking-backend.vercel.app/v1/verify/link?u=x&c=482915", "en").HTML,
		"2-welcome":      s.welcomeEmail(info).HTML,
		"3-streak-start": s.streakStartEmail(info).HTML,
		"4-reminder":     s.streakReminderEmail(info, 5).HTML,
		"5-reminder-ar":  s.streakReminderEmail(ar, 5).HTML,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
