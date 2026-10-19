// Package speech turns a recording into a scored speaking report: Deepgram
// (or ElevenLabs) transcribes it with word timings, Go measures pace, pauses and filler words,
// and Claude writes the scores and feedback.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

// Word is one spoken word with its timing in seconds.
type Word struct {
	Text  string
	Start float64
	End   float64
}

// ErrBadAudio means the speech-to-text service couldn't read the recording.
var ErrBadAudio = errors.New("audio could not be read")

// Transcriber returns the words spoken in a recording.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, filename, contentType, lang string) ([]Word, error)
}

// ElevenLabs uses the Scribe speech-to-text API, which keeps filler words
// ("um", "uh") in its verbatim transcript.
type ElevenLabs struct {
	APIKey  string
	Model   string // defaults to scribe_v2
	BaseURL string // defaults to https://api.elevenlabs.io; tests point it elsewhere
	Client  *http.Client
}

func (e ElevenLabs) Transcribe(ctx context.Context, audio []byte, filename, contentType, lang string) ([]Word, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", contentType)
	fw, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(audio); err != nil {
		return nil, err
	}
	model := e.Model
	if model == "" {
		model = "scribe_v2"
	}
	_ = mw.WriteField("model_id", model)
	_ = mw.WriteField("language_code", lang)
	_ = mw.WriteField("timestamps_granularity", "word")
	_ = mw.WriteField("tag_audio_events", "false")
	// Keep every "um", "uh" and false start: they're what the coach counts.
	_ = mw.WriteField("no_verbatim", "false")
	if err := mw.Close(); err != nil {
		return nil, err
	}

	base := e.BaseURL
	if base == "" {
		base = "https://api.elevenlabs.io"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/speech-to-text", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", e.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	client := e.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		return nil, fmt.Errorf("%w: elevenlabs status %d: %.300s", ErrBadAudio, resp.StatusCode, raw)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("elevenlabs: status %d: %.300s", resp.StatusCode, raw)
	}
	var out struct {
		Words []struct {
			Text  string  `json:"text"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Type  string  `json:"type"`
		} `json:"words"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("elevenlabs: bad response: %w", err)
	}
	words := make([]Word, 0, len(out.Words))
	for _, w := range out.Words {
		if w.Type != "" && w.Type != "word" { // skip spacing and audio events
			continue
		}
		words = append(words, Word{Text: w.Text, Start: w.Start, End: w.End})
	}
	return words, nil
}
