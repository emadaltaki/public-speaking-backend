package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
)

// Brevo sends email through Brevo's HTTP API (free plan: 300 emails a day).
// It needs no SMTP login, only an API key and a sender address confirmed in
// the Brevo dashboard.
type Brevo struct {
	APIKey string
	From   string // "Name <address>" or a bare address
	Client *http.Client
	URL    string // defaults to Brevo's API; tests point it elsewhere
}

func (b Brevo) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(b.From)
	if err != nil {
		return fmt.Errorf("MAIL_FROM: %w", err)
	}
	payload := map[string]any{
		"sender":      map[string]string{"name": from.Name, "email": from.Address},
		"to":          []map[string]string{{"email": m.To}},
		"subject":     m.Subject,
		"textContent": m.Text,
	}
	if m.HTML != "" {
		payload["htmlContent"] = m.HTML
	}
	body, _ := json.Marshal(payload)
	url := b.URL
	if url == "" {
		url = "https://api.brevo.com/v3/smtp/email"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", b.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := b.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("brevo: status %d: %s", resp.StatusCode, msg)
	}
	return nil
}
