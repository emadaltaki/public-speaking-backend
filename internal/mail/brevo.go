package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
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
	if len(m.Headers) > 0 {
		payload["headers"] = m.Headers
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
		return &ProviderError{Status: resp.StatusCode, Body: string(msg)}
	}
	return nil
}

// ProviderError is a rejection from the email provider's API.
type ProviderError struct {
	Status int
	Body   string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("brevo: status %d: %s", e.Status, e.Body)
}

// Hint explains a send failure in a sentence that is safe to show while the
// email settings are being set up. It never includes keys or passwords.
func Hint(err error) string {
	var pe *ProviderError
	switch {
	case errors.As(err, &pe) && pe.Status == http.StatusUnauthorized:
		return "Brevo rejected BREVO_API_KEY. Use an API key (it starts with xkeysib-), not an SMTP key (xsmtpsib-)."
	case errors.As(err, &pe) && strings.Contains(strings.ToLower(pe.Body), "sender"):
		return "Brevo doesn't accept the MAIL_FROM address yet. Add it under Senders in Brevo and click the confirmation email."
	case errors.As(err, &pe):
		var body struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(pe.Body), &body) == nil && body.Message != "" {
			return "Brevo said: " + body.Message
		}
		return fmt.Sprintf("Brevo returned status %d.", pe.Status)
	case err != nil && strings.HasPrefix(err.Error(), "auth:"):
		return "The SMTP login was refused. Check SMTP_USER and SMTP_PASS (Gmail needs an App Password)."
	case err != nil && strings.HasPrefix(err.Error(), "MAIL_FROM:"):
		return "MAIL_FROM isn't a valid address. Use: Speak Up Fearless <you@gmail.com>"
	}
	return ""
}
