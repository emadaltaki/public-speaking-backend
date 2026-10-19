package speech

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

func words(spec ...any) []Word {
	// spec: text, start, end, text, start, end, ...
	var out []Word
	for i := 0; i+2 < len(spec); i += 3 {
		out = append(out, Word{Text: spec[i].(string), Start: spec[i+1].(float64), End: spec[i+2].(float64)})
	}
	return out
}

func TestAnalyzeEnglish(t *testing.T) {
	w := words(
		"So,", 0.0, 0.3, "um,", 0.4, 0.6, "you", 0.7, 0.8, "know", 0.8, 1.0, "I", 1.1, 1.2,
		"like", 1.2, 1.4, "dogs.", 1.4, 1.8, "Uhhh", 4.0, 4.4, "really", 4.5, 4.9, "like", 5.0, 5.2, "them.", 5.2, 6.0,
	)
	tokens, m := Analyze(w, "en")
	if m.WordCount != 11 {
		t.Fatalf("word count %d", m.WordCount)
	}
	want := map[string]int{"um": 1, "uh": 1, "you know": 1, "like": 2}
	for k, v := range want {
		if m.Fillers[k] != v {
			t.Fatalf("fillers %v, want %s=%d", m.Fillers, k, v)
		}
	}
	if m.FillerCount != 5 || !tokens[1].F || !tokens[2].F || !tokens[3].F || tokens[4].F {
		t.Fatalf("filler marks wrong: %d %+v", m.FillerCount, tokens[:5])
	}
	if m.LongPauses != 1 || m.LongestPauseSeconds != 2.2 {
		t.Fatalf("pauses %d longest %v", m.LongPauses, m.LongestPauseSeconds)
	}
	if m.WordsPerMinute != 110 { // 11 words over 6 seconds
		t.Fatalf("wpm %d", m.WordsPerMinute)
	}
	got := PromptTranscript(tokens)
	if !strings.Contains(got, "[0:00] So, {um,} {you} {know} I {like} dogs. [pause 2.2s]\n[0:04] {Uhhh} really") {
		t.Fatalf("prompt transcript:\n%s", got)
	}
}

func TestAnalyzeArabic(t *testing.T) {
	_, m := Analyze(words("يعني", 0.0, 0.3, "أنا", 0.4, 0.6, "اااا", 0.7, 1.0, "أحب", 1.1, 1.4, "القراءة.", 1.5, 2.0), "ar")
	if m.FillerCount != 2 || m.Fillers["يعني"] != 1 || m.Fillers["ااا"] != 1 {
		t.Fatalf("arabic fillers %v", m.Fillers)
	}
}

func TestElevenLabs(t *testing.T) {
	var fields map[string]string
	var key, fileType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("xi-api-key")
		_ = r.ParseMultipartForm(1 << 20)
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		if f := r.MultipartForm.File["file"]; len(f) == 1 {
			fileType = f[0].Header.Get("Content-Type")
		}
		if fields["language_code"] == "xx" {
			http.Error(w, `{"detail":"bad audio"}`, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"words":[{"text":"Hi","start":0.1,"end":0.3,"type":"word"},{"text":" ","start":0.3,"end":0.4,"type":"spacing"},{"text":"there","start":0.4,"end":0.8,"type":"word"}]}`)
	}))
	defer srv.Close()
	e := ElevenLabs{APIKey: "k", BaseURL: srv.URL}
	got, err := e.Transcribe(context.Background(), []byte("audio"), "recording.webm", "audio/webm", "en")
	if err != nil || len(got) != 2 || got[1].Text != "there" || got[1].Start != 0.4 {
		t.Fatalf("transcribe: %v %+v", err, got)
	}
	if key != "k" || fields["model_id"] != "scribe_v2" || fields["timestamps_granularity"] != "word" || fields["no_verbatim"] != "false" || fields["language_code"] != "en" || fileType != "audio/webm" {
		t.Fatalf("request: key=%q fields=%v type=%q", key, fields, fileType)
	}
	if _, err := e.Transcribe(context.Background(), []byte("audio"), "r.webm", "audio/webm", "xx"); !errors.Is(err, ErrBadAudio) {
		t.Fatalf("400 should be ErrBadAudio, got %v", err)
	}
}

func TestClaudeCoach(t *testing.T) {
	feedback := `{"overall":12,"scores":{"pace":{"score":7,"summary":"Good."},"pauses":{"score":0,"summary":"x"},"fillers":{"score":5,"summary":"x"},"clarity":{"score":8,"summary":"x"},"flow":{"score":6,"summary":"x"}},"moments":[{"at":40,"category":"flow","quote":"and then","issue":"i","fix":"f"},{"at":3,"category":"fillers","quote":"um","issue":"i","fix":"f"}],"strengths":["Clear opening"],"nextStep":"Pause after key points."}`
	var req map[string]any
	var beta string
	stop := "end_turn"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		_ = json.NewDecoder(r.Body).Decode(&req)
		text, _ := json.Marshal(feedback)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"`+stop+`","content":[{"type":"text","text":`+string(text)+`}],"usage":{"input_tokens":10,"output_tokens":10}}`)
	}))
	defer srv.Close()

	c := NewClaude("key", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	fb, err := c.Evaluate(context.Background(), CoachInput{Topic: "My hobby", Lang: "ar", DurationSeconds: 30, Transcript: "[0:00] hi"})
	if err != nil {
		t.Fatal(err)
	}
	oc := req["output_config"].(map[string]any)
	if req["model"] != "claude-sonnet-5-5" || req["fallbacks"] != "default" || oc["effort"] != "medium" ||
		oc["format"].(map[string]any)["type"] != "json_schema" || !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Fatalf("request: beta=%q body=%v", beta, req)
	}
	if !strings.Contains(req["system"].([]any)[0].(map[string]any)["text"].(string), "Arabic") {
		t.Fatalf("arabic not requested in system prompt")
	}
	// Clean clamps scores and sorts moments within the recording.
	if fb.Overall != 10 || fb.Scores.Pauses.Score != 1 || fb.Moments[0].At != 3 || fb.Moments[1].At != 30 {
		t.Fatalf("cleaned feedback: %+v", fb)
	}

	stop = "refusal"
	if _, err := c.Evaluate(context.Background(), CoachInput{Transcript: "x"}); !errors.Is(err, ErrDeclined) {
		t.Fatalf("refusal: %v", err)
	}
}

func TestDeepgram(t *testing.T) {
	var q url.Values
	var auth, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, auth, ctype = r.URL.Query(), r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if q.Get("language") == "xx" {
			http.Error(w, `{"err_msg":"bad audio"}`, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"results":{"channels":[{"alternatives":[{"words":[{"word":"um","punctuated_word":"Um,","start":0.1,"end":0.3},{"word":"hello","punctuated_word":"hello.","start":0.4,"end":0.8}]}]}]}}`)
	}))
	defer srv.Close()
	d := Deepgram{APIKey: "k", BaseURL: srv.URL}
	got, err := d.Transcribe(context.Background(), []byte("audio"), "recording.webm", "audio/webm", "en")
	if err != nil || len(got) != 2 || got[0].Text != "Um," || got[1].Start != 0.4 {
		t.Fatalf("transcribe: %v %+v", err, got)
	}
	if auth != "Token k" || ctype != "audio/webm" || q.Get("model") != "nova-3" || q.Get("filler_words") != "true" || q.Get("language") != "en" {
		t.Fatalf("request: auth=%q type=%q query=%v", auth, ctype, q)
	}
	if _, m := Analyze(got, "en"); m.FillerCount != 1 {
		t.Fatalf("filler from deepgram not counted: %+v", m)
	}
	if _, err := d.Transcribe(context.Background(), []byte("audio"), "r.webm", "audio/webm", "xx"); !errors.Is(err, ErrBadAudio) {
		t.Fatalf("400 should be ErrBadAudio, got %v", err)
	}
}

type fixedTranscriber string

func (f fixedTranscriber) Transcribe(context.Context, []byte, string, string, string) ([]Word, error) {
	return []Word{{Text: string(f)}}, nil
}

func TestByLanguage(t *testing.T) {
	b := ByLanguage{Default: fixedTranscriber("deepgram"), ByLang: map[string]Transcriber{"ar": fixedTranscriber("elevenlabs")}}
	en, _ := b.Transcribe(context.Background(), nil, "", "", "en")
	ar, _ := b.Transcribe(context.Background(), nil, "", "", "ar")
	if en[0].Text != "deepgram" || ar[0].Text != "elevenlabs" {
		t.Fatalf("routing: en=%v ar=%v", en, ar)
	}
}

type failingTranscriber struct{ err error }

func (f failingTranscriber) Transcribe(context.Context, []byte, string, string, string) ([]Word, error) {
	return nil, f.err
}

func TestFallback(t *testing.T) {
	ok := Fallback{Primary: failingTranscriber{errors.New("down")}, Backup: fixedTranscriber("backup")}
	if w, err := ok.Transcribe(context.Background(), nil, "", "", "en"); err != nil || w[0].Text != "backup" {
		t.Fatalf("should fall back: %v %v", w, err)
	}
	bad := Fallback{Primary: failingTranscriber{ErrBadAudio}, Backup: fixedTranscriber("backup")}
	if _, err := bad.Transcribe(context.Background(), nil, "", "", "en"); !errors.Is(err, ErrBadAudio) {
		t.Fatalf("bad audio should not fall back: %v", err)
	}
	first := Fallback{Primary: fixedTranscriber("primary"), Backup: fixedTranscriber("backup")}
	if w, _ := first.Transcribe(context.Background(), nil, "", "", "en"); w[0].Text != "primary" {
		t.Fatalf("primary first: %v", w)
	}
}

func TestPaceTimeline(t *testing.T) {
	// 40 words in the first 30 seconds (80 wpm), 80 in the next 30 (160 wpm).
	var w []Word
	for i := 0; i < 40; i++ {
		w = append(w, Word{Text: "word", Start: float64(i) * 0.7, End: float64(i)*0.7 + 0.3})
	}
	for i := 0; i < 80; i++ {
		at := 30 + float64(i)*0.37
		w = append(w, Word{Text: "word", Start: at, End: at + 0.2})
	}
	_, m := Analyze(w, "en")
	if len(m.PaceTimeline) != 2 || m.PaceTimeline[0] != 80 || m.PaceTimeline[1] < 150 || m.PaceTimeline[1] > 175 {
		t.Fatalf("pace timeline %v", m.PaceTimeline)
	}
	// A 30-second talk is one block.
	_, short := Analyze(w[:40], "en")
	if len(short.PaceTimeline) != 1 {
		t.Fatalf("short talk %v", short.PaceTimeline)
	}
}

func TestCleanStory(t *testing.T) {
	fb := Clean(Feedback{Story: Story{Opening: Part{Rating: "great"}, Outline: []string{"a", "b", "c", "d", "e", "f"}}}, 60)
	if fb.Story.Opening.Rating != "ok" || len(fb.Story.Outline) != 5 || fb.Story.Middle.Rating != "ok" {
		t.Fatalf("story not cleaned: %+v", fb.Story)
	}
}

func TestCleanPractice(t *testing.T) {
	if fb := Clean(Feedback{Practice: Practice{Game: "chess"}}, 60); fb.Practice.Game != "triple-step" {
		t.Fatalf("unknown game should fall back: %+v", fb.Practice)
	}
	if fb := Clean(Feedback{Practice: Practice{Game: "conductor"}}, 60); fb.Practice.Game != "conductor" {
		t.Fatalf("known game kept: %+v", fb.Practice)
	}
}
