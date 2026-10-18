// Command api serves the Speak Up Fearless backend.
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

	"github.com/emadaltaki/public-speaking-backend/internal/api"
	"github.com/emadaltaki/public-speaking-backend/internal/auth"
	"github.com/emadaltaki/public-speaking-backend/internal/mail"
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
	if smtpCfg.Host != "" && smtpCfg.User != "" && smtpCfg.Pass != "" && smtpCfg.From != "" {
		mailer = smtpCfg
	} else {
		log.Warn("SMTP_HOST, SMTP_USER, SMTP_PASS or MAIL_FROM not set: verification emails are off")
	}

	srv := api.NewServer(api.Config{
		AllowedOrigins: list(env("ALLOWED_ORIGINS", "https://public-speaking-lemon.vercel.app")),
		AllowLocalhost: env("ALLOW_LOCALHOST", "true") == "true",
		AdminEmails:    list(os.Getenv("ADMIN_EMAILS")),
		Mailer:         mailer,
	}, st, auth.NewFirebaseVerifier(projectID), log)

	httpServer := &http.Server{
		Addr:              ":" + env("PORT", "8080"),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
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
