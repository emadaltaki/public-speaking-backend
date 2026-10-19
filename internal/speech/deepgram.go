package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Deepgram uses Deepgram's pre-recorded speech-to-text API. Filler words
// ("um", "uh") are kept by asking for them explicitly; Deepgram drops them
// otherwise.
type Deepgram struct {
	APIKey  string
	Model   string // defaults to nova-3
	BaseURL string // defaults to https://api.deepgram.com; tests point it elsewhere
	Client  *http.Client
}

func (d Deepgram) Transcribe(ctx context.Context, audio []byte, filename, contentType, lang string) ([]Word, error) {
	model := d.Model
	if model == "" {
		model = "nova-3"
	}
	q := url.Values{}
	q.Set("model", model)
	q.Set("filler_words", "true")
	q.Set("punctuate", "true")
	if lang != "" {
		q.Set("language", lang)
	}
	base := d.BaseURL
	if base == "" {
		base = "https://api.deepgram.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/listen?"+q.Encode(), bytes.NewReader(audio))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+d.APIKey)
	req.Header.Set("Content-Type", contentType)
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		return nil, fmt.Errorf("%w: deepgram status %d: %.300s", ErrBadAudio, resp.StatusCode, raw)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("deepgram: status %d: %.300s", resp.StatusCode, raw)
	}
	var out struct {
		Results struct {
			Channels []struct {
				Alternatives []struct {
					Words []struct {
						Word           string  `json:"word"`
						PunctuatedWord string  `json:"punctuated_word"`
						Start          float64 `json:"start"`
						End            float64 `json:"end"`
					} `json:"words"`
				} `json:"alternatives"`
			} `json:"channels"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("deepgram: bad response: %w", err)
	}
	words := []Word{}
	if len(out.Results.Channels) == 0 || len(out.Results.Channels[0].Alternatives) == 0 {
		return words, nil
	}
	for _, w := range out.Results.Channels[0].Alternatives[0].Words {
		text := w.PunctuatedWord
		if text == "" {
			text = w.Word
		}
		words = append(words, Word{Text: text, Start: w.Start, End: w.End})
	}
	return words, nil
}

// ByLanguage sends each recording to the transcriber set for its language,
// or to Default when none is set.
type ByLanguage struct {
	Default Transcriber
	ByLang  map[string]Transcriber
}

func (b ByLanguage) Transcribe(ctx context.Context, audio []byte, filename, contentType, lang string) ([]Word, error) {
	if t, ok := b.ByLang[lang]; ok && t != nil {
		return t.Transcribe(ctx, audio, filename, contentType, lang)
	}
	return b.Default.Transcribe(ctx, audio, filename, contentType, lang)
}
