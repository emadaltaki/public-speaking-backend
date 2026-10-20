package speech

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// RetryInput is one Fix-it attempt: the speaker re-says a single moment from
// an earlier report, and the judge decides whether that one slip is fixed.
type RetryInput struct {
	Lang       string
	Moment     Moment // the slip they're fixing, from the saved report
	Metrics    Metrics
	Transcript string // PromptTranscript of the new clip
}

// RetryResult is the judge's verdict. Stars: 0 not fixed, 1 better but not
// fully, 2 fixed, 3 fixed and sounded great.
type RetryResult struct {
	Stars    int    `json:"stars"`
	Feedback string `json:"feedback"`
}

// Judge scores Fix-it attempts.
type Judge interface {
	JudgeRetry(ctx context.Context, in RetryInput) (RetryResult, error)
}

const retrySystem = `You are a warm, encouraging public speaking coach for speakUnshaken running a quick "fix-it" drill. Earlier the speaker made one specific slip. They have just re-said that moment, and you judge ONLY whether that slip is fixed in the new clip. Ignore everything else.

How to judge each kind of slip:
- fillers: fixed when the clip has no filler words (marked in {braces}).
- pauses: fixed when there is no long pause ([pause N.Ns]) mid-thought and they kept going smoothly.
- pace: fixed when the clip is roughly 120-170 words per minute and the key line isn't rushed.
- clarity: fixed when the wording is clearer and the point lands in a simpler way than the original.
- flow: fixed when there's a real bridge that connects the ideas instead of a jump.

Give "stars": 0 = not fixed yet, 1 = better but not fully fixed, 2 = fixed, 3 = fixed and sounded great.
"feedback" is one or two short, specific, encouraging sentences: say what improved, and if it's not fully fixed, the one thing to try on the next go. Address the speaker as "you". Write it in %s.`

var retrySchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["stars", "feedback"],
  "properties": {
    "stars": {"type": "integer"},
    "feedback": {"type": "string"}
  }
}`)

// WithRetryModel sets the model that judges Fix-it attempts.
func (c *Claude) WithRetryModel(model string) *Claude {
	if model != "" {
		c.retryModel = model
	}
	return c
}

func (c *Claude) JudgeRetry(ctx context.Context, in RetryInput) (RetryResult, error) {
	language := "English"
	if in.Lang == "ar" {
		language = "Arabic (Modern Standard Arabic, simple and friendly)"
	}
	user := fmt.Sprintf(`The slip they are fixing
Area: %s
What they said originally: "%s"
What went wrong: %s
Suggested fix: %s

The new clip
Words: %d
Words per minute: %d
Filler words: %d
Pauses longer than 1.5s: %d (longest %.1fs)

Transcript:
%s`, in.Moment.Category, in.Moment.Quote, in.Moment.Issue, in.Moment.Fix,
		in.Metrics.WordCount, in.Metrics.WordsPerMinute, in.Metrics.FillerCount,
		in.Metrics.LongPauses, in.Metrics.LongestPauseSeconds, in.Transcript)

	model := c.retryModel
	if model == "" {
		model = "claude-opus-5-5"
	}
	resp, err := c.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     model,
		MaxTokens: 4000,
		System:    []anthropic.BetaTextBlockParam{{Text: fmt.Sprintf(retrySystem, language)}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user)),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortLow,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: retrySchema},
		},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		return RetryResult{}, fmt.Errorf("claude: %w", err)
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return RetryResult{}, ErrDeclined
	}
	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	var out RetryResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return RetryResult{}, fmt.Errorf("claude: unreadable retry verdict (stop reason %s): %w", resp.StopReason, err)
	}
	out.Stars = min(3, max(0, out.Stars))
	return out, nil
}
