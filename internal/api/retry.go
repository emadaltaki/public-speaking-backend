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

	"github.com/emadaltaki/public-speaking-backend/internal/speech"
	"github.com/emadaltaki/public-speaking-backend/internal/store"
)

const (
	speechRetryPath  = "/v1/speech/retry"
	maxRetryUpload   = 1 << 20
	minRetryWords    = 3
	retryDailyLimit  = 60
	retryPassedStars = 2
)

// retrySpeech is Fix-it mode: the speaker re-says one moment from a report
// and gets 0-3 stars for whether that slip is fixed.
func (s *Server) retrySpeech(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Transcriber == nil || s.cfg.Judge == nil {
		speechError(w, http.StatusServiceUnavailable, "not_configured", "fix-it mode isn't set up yet")
		return
	}
	u := userFrom(r.Context())

	if err := r.ParseMultipartForm(maxRetryUpload); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			speechError(w, http.StatusRequestEntityTooLarge, "too_large", "the recording is too large")
			return
		}
		speechError(w, http.StatusBadRequest, "bad_audio", "send the recording as multipart form field \"audio\"")
		return
	}
	defer r.MultipartForm.RemoveAll()

	// The moment comes from the saved report, never from the client.
	reportID, err := strconv.ParseInt(r.FormValue("reportId"), 10, 64)
	if err != nil {
		speechError(w, http.StatusNotFound, "not_found", "report not found")
		return
	}
	row, err := s.store.GetSpeechReport(r.Context(), u.UID, reportID)
	if errors.Is(err, store.ErrNotFound) {
		speechError(w, http.StatusNotFound, "not_found", "report not found")
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
	idx, err := strconv.Atoi(r.FormValue("moment"))
	if err != nil || idx < 0 || idx >= len(body.Moments) {
		speechError(w, http.StatusNotFound, "not_found", "moment not found in this report")
		return
	}
	moment := body.Moments[idx]

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
	lang := body.Lang
	if v := r.FormValue("lang"); v == "en" || v == "ar" {
		lang = v
	}

	used, err := s.store.CountSpeechRetriesSince(r.Context(), u.UID, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.serverError(w, "count speech retries", err)
		return
	}
	if used >= retryDailyLimit {
		speechError(w, http.StatusTooManyRequests, "limit", "you've used today's fix-it tries, come back tomorrow")
		return
	}

	sttCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	words, err := s.cfg.Transcriber.Transcribe(sttCtx, audio, "retry."+ext, contentType, lang)
	cancel()
	audio = nil // never kept
	if errors.Is(err, speech.ErrBadAudio) {
		speechError(w, http.StatusBadRequest, "bad_audio", "the recording couldn't be read")
		return
	}
	if err != nil {
		s.log.Error("retry: transcribe", "err", err)
		speechError(w, http.StatusBadGateway, "upstream", "transcription failed, please try again")
		return
	}
	tokens, metrics := speech.Analyze(words, lang)
	if metrics.WordCount < minRetryWords {
		speechError(w, http.StatusBadRequest, "no_speech", "we couldn't hear enough, try saying the whole line")
		return
	}

	judgeCtx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	verdict, err := s.cfg.Judge.JudgeRetry(judgeCtx, speech.RetryInput{
		Lang: lang, Moment: moment, Metrics: metrics, Transcript: speech.PromptTranscript(tokens),
	})
	cancel()
	if err != nil {
		s.log.Error("retry: judge", "err", err)
		speechError(w, http.StatusBadGateway, "upstream", "the coach couldn't review this try, please try again")
		return
	}
	if err := s.store.AddSpeechRetry(r.Context(), u.UID, reportID, idx, verdict.Stars); err != nil {
		s.log.Error("retry: save attempt", "err", err)
	}

	heard := make([]string, len(tokens))
	for i, t := range tokens {
		heard[i] = t.W
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"passed":   verdict.Stars >= retryPassedStars,
		"stars":    verdict.Stars,
		"feedback": verdict.Feedback,
		"heard":    strings.Join(heard, " "),
		"metrics": map[string]any{
			"wordsPerMinute": metrics.WordsPerMinute,
			"fillerCount":    metrics.FillerCount,
			"longPauses":     metrics.LongPauses,
		},
		"transcript": tokens,
	})
}
