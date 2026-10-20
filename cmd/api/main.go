// Command api serves the speakUnshaken backend.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	netmail "net/mail"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // time zones for streak emails, even where the host has none

	"github.com/emadaltaki/public-speaking-backend/internal/api"
	"github.com/emadaltaki/public-speaking-backend/internal/auth"
	"github.com/emadaltaki/public-speaking-backend/internal/mail"
	"github.com/emadaltaki/public-speaking-backend/internal/speech"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	projectID := env("FIREBASE_PROJECT_ID", "speakup-ca500")

	var st store.Store
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		pg, err := store.OpenPostgres(openCtx, dbURL)
		cancel()
		if err != nil {
			return err
		}
		st = pg
	} else {
		log.Warn("DATABASE_URL not set: using in-memory store, data is lost on restart")
		st = store.NewMemory()
	}
	defer st.Close()

	var mailer mail.Sender
	smtpCfg := mail.SMTP{
		Host: os.Getenv("SMTP_HOST"),
		Port: env("SMTP_PORT", "587"),
		User: os.Getenv("SMTP_USER"),
		Pass: os.Getenv("SMTP_PASS"),
		From: os.Getenv("MAIL_FROM"),
	}
	// Be forgiving: SMTP_USER may be pasted as "Name <address>", spaces in a
	// Gmail App Password are optional, and MAIL_FROM defaults to SMTP_USER.
	if addr, err := netmail.ParseAddress(smtpCfg.User); err == nil {
		smtpCfg.User = addr.Address
	}
	smtpCfg.Pass = strings.ReplaceAll(smtpCfg.Pass, " ", "")
	if smtpCfg.From == "" {
		smtpCfg.From = smtpCfg.User
	}
	// "speakUnshaken me@gmail.com" without angle brackets: add them.
	if _, err := netmail.ParseAddress(smtpCfg.From); err != nil {
		if i := strings.LastIndex(smtpCfg.From, " "); i > 0 && strings.Contains(smtpCfg.From[i+1:], "@") {
			smtpCfg.From = (&netmail.Address{Name: strings.TrimSpace(smtpCfg.From[:i]), Address: smtpCfg.From[i+1:]}).String()
		}
	}
	switch {
	case os.Getenv("BREVO_API_KEY") != "" && smtpCfg.From != "":
		mailer = mail.Brevo{APIKey: os.Getenv("BREVO_API_KEY"), From: smtpCfg.From}
		log.Info("verification emails go through Brevo")
	case smtpCfg.Host != "" && smtpCfg.User != "" && smtpCfg.Pass != "" && smtpCfg.From != "":
		mailer = smtpCfg
		log.Info("verification emails go through SMTP", "host", smtpCfg.Host)
	default:
		log.Warn("no BREVO_API_KEY or SMTP settings (with MAIL_FROM): verification emails are off")
	}

	var transcriber speech.Transcriber
	var coach speech.Coach
	// ElevenLabs Scribe writes down every "um" and "uh", so it goes first when
	// its key is set; Deepgram steps in if ElevenLabs is down. Either key alone
	// also works.
	var elevenlabs, deepgram speech.Transcriber
	if key := os.Getenv("ELEVENLABS_API_KEY"); key != "" {
		elevenlabs = speech.ElevenLabs{APIKey: key, Model: env("ELEVENLABS_STT_MODEL", "scribe_v2")}
	}
	if key := os.Getenv("DEEPGRAM_API_KEY"); key != "" {
		deepgram = speech.Deepgram{APIKey: key, Model: env("DEEPGRAM_MODEL", "nova-3")}
	}
	switch {
	case elevenlabs != nil:
		transcriber = speech.Fallback{Primary: elevenlabs, Backup: deepgram, Log: log}
	case deepgram != nil:
		transcriber = deepgram
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		coach = speech.NewClaude(key).WithModel(env("CLAUDE_COACH_MODEL", "claude-sonnet-5-5"))
	}
	if transcriber == nil || coach == nil {
		log.Warn("DEEPGRAM_API_KEY (or ELEVENLABS_API_KEY) or ANTHROPIC_API_KEY not set: the speaking coach is off")
	}

	srv := api.NewServer(api.Config{
		AllowedOrigins:  list(env("ALLOWED_ORIGINS", "https://speakunshaken.com,https://www.speakunshaken.com,https://public-speaking-lemon.vercel.app,https://speakup-ca500.web.app,https://speakup-ca500.firebaseapp.com,https://speakup-dev.web.app,https://speakup-dev.firebaseapp.com,https://dev.speakunshaken.com")),
		AllowLocalhost:  env("ALLOW_LOCALHOST", "true") == "true",
		AdminEmails:     list(os.Getenv("ADMIN_EMAILS")),
		Mailer:          mailer,
		SiteURL:         strings.TrimRight(env("SITE_URL", "https://speakup-ca500.web.app"), "/"),
		PublicURL:       strings.TrimRight(env("PUBLIC_URL", "https://public-speaking-backend.vercel.app"), "/"),
		CronSecret:      os.Getenv("CRON_SECRET"),
		EmailMode:       emailMode(os.Getenv("STREAK_EMAILS")),
		EmailTestList:   list(unquote(os.Getenv("STREAK_EMAILS_TEST"))),
		DefaultTimeZone: env("DEFAULT_TIME_ZONE", "America/Toronto"),
		Transcriber:     transcriber,
		Coach:           coach,
	}, st, auth.NewFirebaseVerifier(projectID), log)

	httpServer := &http.Server{
		Addr:              ":" + env("PORT", "8080"),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      150 * time.Second, // the speaking coach can take a minute or two
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", httpServer.Addr, "firebaseProject", projectID)
		errc <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// emailMode reads STREAK_EMAILS: "on", "test" or anything else for "off".
func emailMode(v string) string {
	switch strings.ToLower(unquote(v)) {
	case "on":
		return "on"
	case "test":
		return "test"
	}
	return "off"
}

// unquote drops quotes pasted around a setting's value, like "test".
func unquote(v string) string {
	return strings.Trim(strings.TrimSpace(v), "\"'")
}
