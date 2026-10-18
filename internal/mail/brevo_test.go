package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrevoSend(t *testing.T) {
	var got map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("api-key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	b := Brevo{APIKey: "k", From: "Speak Up Fearless <me@example.com>", URL: srv.URL}
	if err := b.Send(context.Background(), Message{To: "you@example.com", Subject: "S", Text: "T", HTML: "<b>H</b>"}); err != nil {
		t.Fatal(err)
	}
	sender := got["sender"].(map[string]any)
	if key != "k" || sender["name"] != "Speak Up Fearless" || sender["email"] != "me@example.com" ||
		got["htmlContent"] != "<b>H</b>" || got["textContent"] != "T" {
		t.Fatalf("unexpected request: key=%q body=%v", key, got)
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"sender not valid"}`, http.StatusBadRequest)
	}))
	defer fail.Close()
	b.URL = fail.URL
	err := b.Send(context.Background(), Message{To: "you@example.com"})
	if err == nil || !strings.Contains(Hint(err), "Senders") {
		t.Fatalf("sender error hint: %v / %q", err, Hint(err))
	}
	if h := Hint(&ProviderError{Status: 401, Body: `{"message":"Key not found"}`}); !strings.Contains(h, "xkeysib-") {
		t.Fatalf("401 hint: %q", h)
	}
}
