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
	"strconv"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/emadaltaki/public-speaking-backend/internal/speech"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

type fakeJudge struct {
	in    speech.RetryInput
	stars int
}

func (f *fakeJudge) JudgeRetry(_ context.Context, in speech.RetryInput) (speech.RetryResult, error) {
	f.in = in
	return speech.RetryResult{Stars: f.stars, Feedback: "Much smoother!"}, nil
}

func retryUpload(t *testing.T, h http.Handler, token string, audio []byte, fields map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if audio != nil {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", `form-data; name="audio"; filename="clip"`)
		hdr.Set("Content-Type", "audio/webm")
		fw, _ := mw.CreatePart(hdr)
		fw.Write(audio)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/v1/speech/retry", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func newRetryServer(t *testing.T, stt *fakeSTT, judge *fakeJudge) (http.Handler, *store.Memory, string) {
	t.Helper()
	st := store.NewMemory()
	cfg := Config{Transcriber: stt}
	if judge != nil {
		cfg.Judge = judge
	}
	v := fakeVerifier{"alice": {UID: "alice"}, "bob": {UID: "bob"}}
	h := NewServer(cfg, st, v, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	do(t, h, "GET", "/v1/me", "alice", nil)
	report, _ := json.Marshal(speechBody{Lang: "en", Moments: []speech.Moment{
		{At: 3, Category: "fillers", Quote: "um so basically", Issue: "fillers before the point", Fix: "pause instead of um"},
		{At: 9, Category: "pace", Quote: "and the results", Issue: "rushed", Fix: "slow down on the key line"},
	}})
	saved, _ := st.AddSpeechReport(context.Background(), "alice", report)
	return h, st, strconv.FormatInt(saved.ID, 10)
}

func TestSpeechRetry(t *testing.T) {
	stt := &fakeSTT{words: []speech.Word{
		{Text: "So", Start: 0, End: 0.2}, {Text: "the", Start: 0.3, End: 0.4},
		{Text: "results", Start: 0.5, End: 0.9}, {Text: "matter.", Start: 1.0, End: 1.4},
	}}
	judge := &fakeJudge{stars: 3}
	h, _, id := newRetryServer(t, stt, judge)

	rec, body := retryUpload(t, h, "alice", []byte("clip"), map[string]string{"reportId": id, "moment": "0", "lang": "en"})
	if rec.Code != 200 || body["passed"] != true || body["stars"] != 3.0 || body["feedback"] != "Much smoother!" || body["heard"] != "So the results matter." {
		t.Fatalf("retry: %d %v", rec.Code, body)
	}
	m := body["metrics"].(map[string]any)
	if m["fillerCount"] != 0.0 || m["longPauses"] != 0.0 || len(body["transcript"].([]any)) != 4 {
		t.Fatalf("metrics/transcript: %v", body)
	}
	// The moment came from the saved report, not the request.
	if judge.in.Moment.Category != "fillers" || judge.in.Moment.Quote != "um so basically" {
		t.Fatalf("judge got %+v", judge.in.Moment)
	}

	judge.stars = 1
	_, body = retryUpload(t, h, "alice", []byte("clip"), map[string]string{"reportId": id, "moment": "1"})
	if body["passed"] != false || body["stars"] != 1.0 {
		t.Fatalf("partial: %v", body)
	}
	_, report := do(t, h, "GET", "/v1/speech/reports/"+id, "alice", nil)
	fixes := report["fixes"].(map[string]any)
	if fixes["0"] != 3.0 || fixes["1"] != 1.0 {
		t.Fatalf("fixes: %v", fixes)
	}
}

func TestSpeechRetryErrors(t *testing.T) {
	stt := &fakeSTT{words: spokenWords(10)}
	if h, _, id := newRetryServer(t, stt, nil); true {
		if rec, body := retryUpload(t, h, "alice", []byte("a"), map[string]string{"reportId": id, "moment": "0"}); rec.Code != 503 || body["code"] != "not_configured" {
			t.Fatalf("not configured: %d %v", rec.Code, body)
		}
	}
	h, st, id := newRetryServer(t, stt, &fakeJudge{stars: 2})
	for name, f := range map[string]map[string]string{
		"other user": {"reportId": id, "moment": "0"},
		"bad index":  {"reportId": id, "moment": "5"},
		"no report":  {"reportId": "999", "moment": "0"},
	} {
		token := "alice"
		if name == "other user" {
			token = "bob"
		}
		if rec, body := retryUpload(t, h, token, []byte("a"), f); rec.Code != 404 || body["code"] != "not_found" {
			t.Fatalf("%s: %d %v", name, rec.Code, body)
		}
	}
	if rec, body := retryUpload(t, h, "alice", bytes.Repeat([]byte("a"), 2<<20), map[string]string{"reportId": id, "moment": "0"}); rec.Code != 413 || body["code"] != "too_large" {
		t.Fatalf("too large: %d %v", rec.Code, body)
	}
	stt.words = spokenWords(2)
	if rec, body := retryUpload(t, h, "alice", []byte("a"), map[string]string{"reportId": id, "moment": "0"}); rec.Code != 400 || body["code"] != "no_speech" {
		t.Fatalf("no speech: %d %v", rec.Code, body)
	}
	stt.words = spokenWords(10)
	rid, _ := strconv.ParseInt(id, 10, 64)
	for i := 0; i < retryDailyLimit; i++ {
		_ = st.AddSpeechRetry(context.Background(), "alice", rid, 0, 1)
	}
	if rec, body := retryUpload(t, h, "alice", []byte("a"), map[string]string{"reportId": id, "moment": "0"}); rec.Code != 429 || body["code"] != "limit" {
		t.Fatalf("limit: %d %v", rec.Code, body)
	}
}

func TestClaudeJudgeRequest(t *testing.T) {
	var req map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"{\"stars\":7,\"feedback\":\"Nice.\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	j := speech.NewClaude("k", optBaseURL(srv.URL)...)
	res, err := j.JudgeRetry(context.Background(), speech.RetryInput{Lang: "en", Moment: speech.Moment{Category: "pace"}, Transcript: "[0:00] hi"})
	if err != nil || res.Stars != 3 {
		t.Fatalf("judge: %v %+v", err, res)
	}
	oc := req["output_config"].(map[string]any)
	if req["model"] != "claude-opus-5-5" || oc["effort"] != "low" || req["fallbacks"] != "default" ||
		!strings.Contains(req["system"].([]any)[0].(map[string]any)["text"].(string), "fix-it") {
		t.Fatalf("request: %v", req)
	}
}

func optBaseURL(u string) []option.RequestOption {
	return []option.RequestOption{option.WithBaseURL(u), option.WithMaxRetries(0)}
}
