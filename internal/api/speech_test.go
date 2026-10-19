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
	"strings"
	"testing"

	"github.com/emadaltaki/public-speaking-backend/internal/speech"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

type fakeSTT struct {
	words []speech.Word
	err   error
	got   struct{ name, ctype, lang string }
}

func (f *fakeSTT) Transcribe(_ context.Context, _ []byte, name, ctype, lang string) ([]speech.Word, error) {
	f.got.name, f.got.ctype, f.got.lang = name, ctype, lang
	return f.words, f.err
}

type fakeCoach struct{ in speech.CoachInput }

func (f *fakeCoach) Evaluate(_ context.Context, in speech.CoachInput) (speech.Feedback, error) {
	f.in = in
	s := speech.Score{Score: 7, Summary: "ok"}
	return speech.Clean(speech.Feedback{
		Overall: 7, Scores: speech.Scores{Pace: s, Pauses: s, Fillers: s, Clarity: s, Flow: s},
		Moments:   []speech.Moment{{At: 2, Category: "fillers", Quote: "um", Issue: "filler", Fix: "pause instead"}},
		Strengths: []string{"Warm tone"}, NextStep: "Slow down.",
	}, in.DurationSeconds), nil
}

func spokenWords(n int) []speech.Word {
	out := make([]speech.Word, n)
	for i := range out {
		out[i] = speech.Word{Text: "word", Start: float64(i) * 0.4, End: float64(i)*0.4 + 0.3}
	}
	out[1].Text = "يعني"
	return out
}

func newSpeechServer(t *testing.T, stt *fakeSTT, coach *fakeCoach) (http.Handler, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	cfg := Config{}
	if stt != nil {
		cfg.Transcriber = stt
	}
	if coach != nil {
		cfg.Coach = coach
	}
	v := fakeVerifier{"alice": {UID: "alice"}, "bob": {UID: "bob"}}
	return NewServer(cfg, st, v, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler(), st
}

func upload(t *testing.T, h http.Handler, token string, audio []byte, ctype string, fields map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if audio != nil {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", `form-data; name="audio"; filename="clip"`)
		hdr.Set("Content-Type", ctype)
		fw, _ := mw.CreatePart(hdr)
		fw.Write(audio)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/v1/speech/analyze", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestSpeechAnalyze(t *testing.T) {
	stt := &fakeSTT{words: spokenWords(40)}
	coach := &fakeCoach{}
	h, _ := newSpeechServer(t, stt, coach)

	rec, body := upload(t, h, "alice", []byte("fake-audio"), "audio/webm;codecs=opus",
		map[string]string{"topic": "My weekend", "lang": "ar", "durationSeconds": "20"})
	if rec.Code != 200 {
		t.Fatalf("analyze: %d %v", rec.Code, body)
	}
	if stt.got.ctype != "audio/webm" || stt.got.lang != "ar" || stt.got.name != "recording.webm" {
		t.Fatalf("transcriber got %+v", stt.got)
	}
	if coach.in.Topic != "My weekend" || !strings.Contains(coach.in.Transcript, "{يعني}") || coach.in.DurationSeconds != 20 {
		t.Fatalf("coach got %+v", coach.in)
	}
	for _, k := range []string{"id", "createdAt", "topic", "lang", "durationSeconds", "overall", "scores", "metrics", "moments", "strengths", "nextStep", "transcript"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("response missing %q: %v", k, body)
		}
	}
	m := body["metrics"].(map[string]any)
	if m["wordCount"] != 40.0 || m["fillerCount"] != 1.0 || body["scores"].(map[string]any)["flow"].(map[string]any)["score"] != 7.0 {
		t.Fatalf("metrics/scores: %v %v", m, body["scores"])
	}
	tr := body["transcript"].([]any)
	if len(tr) != 40 || tr[1].(map[string]any)["f"] != true {
		t.Fatalf("transcript: %v", tr[:2])
	}
	id := body["id"].(string)

	// History, the full report, and the practice session it counts as.
	_, list := do(t, h, "GET", "/v1/speech/reports", "alice", nil)
	reports := list["reports"].([]any)
	if len(reports) != 1 || reports[0].(map[string]any)["id"] != id || reports[0].(map[string]any)["overall"] != 7.0 {
		t.Fatalf("list: %v", list)
	}
	rec, full := do(t, h, "GET", "/v1/speech/reports/"+id, "alice", nil)
	if rec.Code != 200 || full["nextStep"] != "Slow down." || len(full["transcript"].([]any)) != 40 {
		t.Fatalf("get: %d %v", rec.Code, full)
	}
	if rec, _ := do(t, h, "GET", "/v1/speech/reports/"+id, "bob", nil); rec.Code != 404 {
		t.Fatalf("other user's report: %d", rec.Code)
	}
	_, prog := do(t, h, "GET", "/v1/games/progress", "alice", nil)
	g := prog["games"].([]any)
	if len(g) != 1 || g[0].(map[string]any)["gameId"] != "speech-coach" || g[0].(map[string]any)["totalSeconds"] != 20.0 {
		t.Fatalf("progress: %v", prog)
	}
}

func TestSpeechErrors(t *testing.T) {
	if h, _ := newSpeechServer(t, nil, nil); true {
		if rec, body := upload(t, h, "alice", []byte("a"), "audio/webm", nil); rec.Code != 503 || body["code"] != "not_configured" {
			t.Fatalf("not configured: %d %v", rec.Code, body)
		}
	}
	stt := &fakeSTT{words: spokenWords(5)}
	h, st := newSpeechServer(t, stt, &fakeCoach{})
	if rec, body := upload(t, h, "alice", []byte("a"), "audio/webm", nil); rec.Code != 400 || body["code"] != "no_speech" {
		t.Fatalf("no speech: %d %v", rec.Code, body)
	}
	if rec, body := upload(t, h, "alice", []byte("a"), "image/png", nil); rec.Code != 400 || body["code"] != "bad_audio" {
		t.Fatalf("bad type: %d %v", rec.Code, body)
	}
	if rec, body := upload(t, h, "alice", nil, "", nil); rec.Code != 400 || body["code"] != "bad_audio" {
		t.Fatalf("missing audio: %d %v", rec.Code, body)
	}
	if rec, body := upload(t, h, "alice", bytes.Repeat([]byte("a"), 5<<20), "audio/webm", nil); rec.Code != 413 || body["code"] != "too_large" {
		t.Fatalf("too large: %d %v", rec.Code, body)
	}
	stt.err = speech.ErrBadAudio
	if rec, body := upload(t, h, "alice", []byte("a"), "audio/mp4", nil); rec.Code != 400 || body["code"] != "bad_audio" {
		t.Fatalf("unreadable: %d %v", rec.Code, body)
	}
	stt.err = io.ErrUnexpectedEOF
	if rec, body := upload(t, h, "alice", []byte("a"), "audio/mp4", nil); rec.Code != 502 || body["code"] != "upstream" {
		t.Fatalf("upstream: %d %v", rec.Code, body)
	}
	for i := 0; i < speechDailyLimit; i++ {
		_, _ = st.AddSpeechReport(context.Background(), "alice", json.RawMessage(`{}`))
	}
	if rec, body := upload(t, h, "alice", []byte("a"), "audio/webm", nil); rec.Code != 429 || body["code"] != "limit" {
		t.Fatalf("daily limit: %d %v", rec.Code, body)
	}
	if rec, _ := do(t, h, "GET", "/v1/speech/reports/nope", "alice", nil); rec.Code != 404 {
		t.Fatalf("bad id: %d", rec.Code)
	}
}
