package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if err := b.Send(context.Background(), Message{To: "you@example.com"}); err == nil {
		t.Fatal("expected an error from a 400 response")
	}
}
