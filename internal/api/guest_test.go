package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/emadaltaki/public-speaking-backend/internal/speech"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

func guestUpload(t *testing.T, h http.Handler, guestID, ip string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("lang", "en")
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="audio"; filename="clip"`)
	hdr.Set("Content-Type", "audio/webm")
	fw, _ := mw.CreatePart(hdr)
	fw.Write([]byte("audio"))
	mw.Close()
	req := httptest.NewRequest("POST", "/v1/speech/analyze", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if guestID != "" {
		req.Header.Set("X-Guest-Id", guestID)
	}
	req.Header.Set("X-Real-Ip", ip)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestGuestAnalyze(t *testing.T) {
	stt := &fakeSTT{words: spokenWords(40)}
	h, st := newSpeechServer(t, stt, &fakeCoach{})

	if rec, _ := guestUpload(t, h, "", "1.1.1.1"); rec.Code != 401 {
		t.Fatalf("no token and no guest id: %d", rec.Code)
	}
	rec, body := guestUpload(t, h, "guest-a", "1.1.1.1")
	if rec.Code != 200 || body["id"] != "" || body["overall"] == nil || body["transcript"] == nil {
		t.Fatalf("guest try: %d %v", rec.Code, body)
	}
	// Nothing is saved for guests.
	if rows, _ := st.ListSpeechReports(context.Background(), "", 10); len(rows) != 0 {
		t.Fatalf("guest report was saved")
	}
	// One try per guest id, and per IP even with a new guest id.
	if rec, body := guestUpload(t, h, "guest-a", "2.2.2.2"); rec.Code != 429 || body["code"] != "guest_limit" {
		t.Fatalf("same guest: %d %v", rec.Code, body)
	}
	if rec, body := guestUpload(t, h, "guest-b", "1.1.1.1"); rec.Code != 429 || body["code"] != "guest_limit" {
		t.Fatalf("same IP: %d %v", rec.Code, body)
	}

	// A failed try doesn't use up the guest's free try.
	stt.err = speech.ErrBadAudio
	if rec, _ := guestUpload(t, h, "guest-c", "3.3.3.3"); rec.Code != 400 {
		t.Fatalf("bad audio: %d", rec.Code)
	}
	stt.err = nil
	if rec, body := guestUpload(t, h, "guest-c", "3.3.3.3"); rec.Code != 200 {
		t.Fatalf("retry after failure: %d %v", rec.Code, body)
	}

	// Signed-in users are unaffected and the retry endpoint stays sign-in only.
	if rec, _ := upload(t, h, "alice", []byte("a"), "audio/webm", nil); rec.Code != 200 {
		t.Fatalf("signed-in: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/v1/speech/retry", nil)
	req.Header.Set("X-Guest-Id", "guest-z")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("guest retry: %d", rr.Code)
	}
}

func TestCORSAllowsGuestHeader(t *testing.T) {
	s := NewServer(Config{AllowedOrigins: []string{"https://speakunshaken.com"}}, store.NewMemory(), fakeVerifier{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest("OPTIONS", "/v1/speech/analyze", nil)
	req.Header.Set("Origin", "https://speakunshaken.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !bytes.Contains([]byte(got), []byte("X-Guest-Id")) {
		t.Fatalf("allow headers: %q", got)
	}
}
