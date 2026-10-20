package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emadaltaki/public-speaking-backend/internal/speech"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

const (
	maxSpeechUpload   = 4 << 20 // Vercel rejects request bodies over 4.5 MB
	maxSpeechSeconds  = 125     // the site records one minute; this leaves room for older clients
	minSpeechWords    = 15
	speechDailyLimit  = 15
	speechGameID      = "speech-coach"
	speechAnalyzePath = "/v1/speech/analyze"
)

// Browsers record webm/opus (Chrome, Android) or mp4/AAC (iPhone Safari).
var speechAudioTypes = map[string]string{
	"audio/webm": "webm", "video/webm": "webm", "audio/ogg": "ogg", "audio/mp4": "mp4",
	"video/mp4": "mp4", "audio/x-m4a": "m4a", "audio/m4a": "m4a", "audio/aac": "aac",
	"audio/mpeg": "mp3", "audio/wav": "wav", "audio/x-wav": "wav", "audio/wave": "wav",
}

// speechBody is a saved report; id and createdAt come from the database row.
type speechBody struct {
	Topic           string           `json:"topic"`
	Style           string           `json:"style,omitempty"`
	Lang            string           `json:"lang"`
	DurationSeconds float64          `json:"durationSeconds"`
	Overall         int              `json:"overall"`
	Scores          speech.Scores    `json:"scores"`
	Story           *speech.Story    `json:"story,omitempty"`
	Practice        *speech.Practice `json:"practice,omitempty"`
	Metrics         *speech.Metrics  `json:"metrics"`
	Moments         []speech.Moment  `json:"moments"`
	Strengths       []string         `json:"strengths"`
	NextStep        string           `json:"nextStep"`
	Transcript      []speech.Token   `json:"transcript"`
}

type speechReport struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	speechBody
	// Fixes is the best Fix-it stars per moment index, e.g. {"0": 3}.
	Fixes map[string]int `json:"fixes,omitempty"`
}

type speechScoreOnly struct {
	Score int `json:"score"`
}

type speechSummary struct {
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	Topic           string    `json:"topic"`
	Lang            string    `json:"lang"`
	DurationSeconds float64   `json:"durationSeconds"`
	Overall         int       `json:"overall"`
	Scores          struct {
		Pace    speechScoreOnly `json:"pace"`
		Pauses  speechScoreOnly `json:"pauses"`
		Fillers speechScoreOnly `json:"fillers"`
		Clarity speechScoreOnly `json:"clarity"`
		Flow    speechScoreOnly `json:"flow"`
	} `json:"scores"`
}

func speechError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func (s *Server) analyzeSpeech(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Transcriber == nil || s.cfg.Coach == nil {
		speechError(w, http.StatusServiceUnavailable, "not_configured", "the speaking coach isn't set up yet")
		return
	}
	u := userFrom(r.Context())

	if err := r.ParseMultipartForm(maxSpeechUpload); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			speechError(w, http.StatusRequestEntityTooLarge, "too_large", "the recording is too large")
			return
		}
		speechError(w, http.StatusBadRequest, "bad_audio", "send the recording as multipart form field \"audio\"")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("audio")
	if err != nil {
		speechError(w, http.StatusBadRequest, "bad_audio", "send the recording as multipart form field \"audio\"")
		return
	}
	audio, err := io.ReadAll(file)
	file.Close()
	if err != nil || len(audio) == 0 {
		speechError(w, http.StatusBadRequest, "bad_audio", "the recording is empty")
		return
	}
	contentType := strings.TrimSpace(strings.SplitN(header.Header.Get("Content-Type"), ";", 2)[0])
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = strings.SplitN(http.DetectContentType(audio), ";", 2)[0]
	}
	ext, ok := speechAudioTypes[strings.ToLower(contentType)]
	if !ok {
		speechError(w, http.StatusBadRequest, "bad_audio", "unsupported audio type "+contentType)
		return
	}

	lang := "en"
	if r.FormValue("lang") == "ar" {
		lang = "ar"
	}
	topic := strings.TrimSpace(r.FormValue("topic"))
	style := r.FormValue("style")
	if !speech.Styles(style) {
		style = ""
	}
	if utf8.RuneCountInString(topic) > 200 {
		topic = string([]rune(topic)[:200])
	}
	duration, _ := strconv.ParseFloat(r.FormValue("durationSeconds"), 64)
	if duration > maxSpeechSeconds+5 {
		speechError(w, http.StatusBadRequest, "bad_audio", "recordings can be at most 2 minutes")
		return
	}

	used, err := s.store.CountSpeechReportsSince(r.Context(), u.UID, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.serverError(w, "count speech reports", err)
		return
	}
	if used >= speechDailyLimit {
		speechError(w, http.StatusTooManyRequests, "limit", "you've used today's "+strconv.Itoa(speechDailyLimit)+" analyses, try again tomorrow")
		return
	}

	sttCtx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	words, err := s.cfg.Transcriber.Transcribe(sttCtx, audio, "recording."+ext, contentType, lang)
	cancel()
	audio = nil // never kept: only the transcript is saved
	if errors.Is(err, speech.ErrBadAudio) {
		s.log.Warn("speech: bad audio", "err", err)
		speechError(w, http.StatusBadRequest, "bad_audio", "the recording couldn't be read")
		return
	}
	if err != nil {
		s.log.Error("speech: transcribe", "err", err)
		speechError(w, http.StatusBadGateway, "upstream", "transcription failed, please try again")
		return
	}
	tokens, metrics := speech.Analyze(words, lang)
	if metrics.WordCount < minSpeechWords {
		speechError(w, http.StatusBadRequest, "no_speech", "we couldn't hear enough speech, try speaking a little longer")
		return
	}
	if duration <= 0 {
		duration = tokens[len(tokens)-1].E
	}
	duration = min(duration, maxSpeechSeconds)

	coachCtx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	fb, err := s.cfg.Coach.Evaluate(coachCtx, speech.CoachInput{
		Topic: topic, Style: style, Lang: lang, DurationSeconds: duration, Metrics: metrics,
		Transcript: speech.PromptTranscript(tokens),
	})
	cancel()
	if err != nil {
		s.log.Error("speech: coach", "err", err)
		speechError(w, http.StatusBadGateway, "upstream", "the coach couldn't review this recording, please try again")
		return
	}

	body := speechBody{
		Topic: topic, Style: style, Lang: lang, DurationSeconds: duration, Overall: fb.Overall, Scores: fb.Scores, Story: &fb.Story, Practice: &fb.Practice,
		Metrics: &metrics, Moments: fb.Moments, Strengths: fb.Strengths, NextStep: fb.NextStep, Transcript: tokens,
	}
	raw, _ := json.Marshal(body)
	saved, err := s.store.AddSpeechReport(r.Context(), u.UID, raw)
	if err != nil {
		s.serverError(w, "save speech report", err)
		return
	}
	// Counts towards practice totals and history like the games do.
	details, _ := json.Marshal(map[string]any{"topic": topic, "overall": fb.Overall, "reportId": strconv.FormatInt(saved.ID, 10)})
	overall := fb.Overall
	if _, err := s.store.AddGameSession(r.Context(), store.GameSession{
		UID: u.UID, GameID: speechGameID, DurationSeconds: int(duration + 0.5), Score: &overall, Details: details,
	}); err != nil {
		s.log.Error("speech: add game session", "err", err)
	}
	writeJSON(w, http.StatusOK, speechReport{ID: strconv.FormatInt(saved.ID, 10), CreatedAt: saved.CreatedAt, speechBody: body})
}

func (s *Server) listSpeechReports(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListSpeechReports(r.Context(), userFrom(r.Context()).UID, queryInt(r, "limit", 50, 1, 200))
	if err != nil {
		s.serverError(w, "list speech reports", err)
		return
	}
	out := make([]speechSummary, 0, len(rows))
	for _, row := range rows {
		var sum speechSummary
		if err := json.Unmarshal(row.Report, &sum); err != nil {
			s.log.Error("speech: bad saved report", "id", row.ID, "err", err)
			continue
		}
		sum.ID, sum.CreatedAt = strconv.FormatInt(row.ID, 10), row.CreatedAt
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": out})
}

func (s *Server) getSpeechReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}
	row, err := s.store.GetSpeechReport(r.Context(), userFrom(r.Context()).UID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}
	if err != nil {
		s.serverError(w, "get speech report", err)
		return
	}
	var body speechBody
	if err := json.Unmarshal(row.Report, &body); err != nil {
		s.serverError(w, "decode speech report", err)
		return
	}
	out := speechReport{ID: strconv.FormatInt(row.ID, 10), CreatedAt: row.CreatedAt, speechBody: body, Fixes: map[string]int{}}
	best, err := s.store.BestRetryStars(r.Context(), userFrom(r.Context()).UID, row.ID)
	if err != nil {
		s.log.Error("best retry stars", "err", err)
	}
	for m, stars := range best {
		out.Fixes[strconv.Itoa(m)] = stars
	}
	writeJSON(w, http.StatusOK, out)
}
