package speech

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Score is one 1-10 rating with a short explanation.
type Score struct {
	Score   int    `json:"score"`
	Summary string `json:"summary"`
}

type Scores struct {
	Pace    Score `json:"pace"`
	Pauses  Score `json:"pauses"`
	Fillers Score `json:"fillers"`
	Clarity Score `json:"clarity"`
	Flow    Score `json:"flow"`
}

// Moment points at one place in the recording to work on.
type Moment struct {
	At       float64 `json:"at"`
	Category string  `json:"category"`
	Quote    string  `json:"quote"`
	Issue    string  `json:"issue"`
	Fix      string  `json:"fix"`
}

// Part is one stage of the talk's structure.
type Part struct {
	Rating string `json:"rating"` // strong, ok or weak
	Note   string `json:"note"`
}

// Story is how the talk hangs together: its main points in order, how well
// the opening, middle and ending work, and how the ideas connect.
type Story struct {
	Outline    []string `json:"outline"`
	Opening    Part     `json:"opening"`
	Middle     Part     `json:"middle"`
	Ending     Part     `json:"ending"`
	Connection string   `json:"connection"`
}

// Feedback is what the coach writes.
type Feedback struct {
	Overall   int      `json:"overall"`
	Scores    Scores   `json:"scores"`
	Story     Story    `json:"story"`
	Moments   []Moment `json:"moments"`
	Strengths []string `json:"strengths"`
	NextStep  string   `json:"nextStep"`
}

type CoachInput struct {
	Topic           string
	Lang            string
	DurationSeconds float64
	Metrics         Metrics
	Transcript      string // PromptTranscript output
}

// Coach scores a transcript.
type Coach interface {
	Evaluate(ctx context.Context, in CoachInput) (Feedback, error)
}

// ErrDeclined means the model declined to evaluate this recording.
var ErrDeclined = errors.New("the coach declined to evaluate this recording")

// Claude scores speeches with the Claude API.
type Claude struct {
	client anthropic.Client
	model  string
}

func NewClaude(apiKey string, opts ...option.RequestOption) *Claude {
	return &Claude{
		client: anthropic.NewClient(append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)...),
		model:  "claude-sonnet-5-5",
	}
}

// WithModel switches the Claude model, for example back to claude-opus-5-5.
func (c *Claude) WithModel(model string) *Claude {
	if model != "" {
		c.model = model
	}
	return c
}

const coachSystem = `You are a warm, honest public speaking coach for Speak Up Fearless, a community that helps people who feel anxious about speaking. You review one short practice recording at a time.

You receive the topic, measurements taken from the audio, and a timed transcript. In the transcript, [m:ss] marks when each sentence starts, {braces} mark filler words, and [pause N.Ns] marks a silence longer than 1.5 seconds. The transcript comes from speech-to-text, so ignore small spelling or punctuation errors that a listener wouldn't hear.

Score each area from 1 to 10:
- pace: speaking speed and how steady it is. Roughly 130-160 words per minute is comfortable for a talk; well above or below costs points, and so does rushing through key ideas.
- pauses: whether pauses help (after a key point, before a new idea) or hurt (mid-sentence, searching for words). Very few pauses can also mean rushing.
- fillers: how often filler words appear relative to the length of the talk. Two or three in two minutes is excellent.
- clarity: whether each sentence is easy to follow and the main point is clear.
- flow: how well words and ideas connect: transitions, logical order, sentences that build on each other, a clear start and finish.
"overall" is your holistic 1-10 judgement, not a strict average.

Pace: the measurements include words per minute for each 15-second stretch. Use them to say whether the speaker kept a steady pace or sped up (often when nervous or near the end) or slowed down, and mention when.

Filler words: every word in {braces} was counted as a filler. Count all of them, notice whether they cluster (at the start, between ideas, when searching for words), and name the most frequent ones in the fillers summary.

"story" describes how the talk hangs together:
- "outline": the main points in the order the speaker made them, 2 to 5 short phrases in your own words.
- "opening", "middle", "ending": each has "rating" (strong, ok or weak) and a one-sentence "note". A strong opening hooks the listener and says what the talk is about; a strong middle develops one idea at a time with an example or story; a strong ending lands a clear final point instead of trailing off.
- "connection": two sentences on how well the ideas link to each other and back to the topic, naming a transition that worked or a jump that felt abrupt.

Be encouraging but honest: this person is practising to get better, and vague praise doesn't help them. Each summary is one or two sentences about this recording specifically.

"moments" lists 3 to 8 specific places worth working on, in time order. For each: "at" is the start time in seconds of the sentence it comes from (from the [m:ss] mark), "category" is the area it belongs to, "quote" copies the speaker's exact words from the transcript (a short phrase, without the braces), "issue" says what went wrong, and "fix" gives one concrete thing to do differently next time, such as a better transition phrase or where to pause instead.
"strengths" lists 1 to 3 things they genuinely did well. "nextStep" is the single most useful thing to practise in their next recording.

Write every text field in %s. Address the speaker directly as "you".`

var feedbackSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["overall", "scores", "story", "moments", "strengths", "nextStep"],
  "properties": {
    "overall": {"type": "integer"},
    "story": {
      "type": "object",
      "additionalProperties": false,
      "required": ["outline", "opening", "middle", "ending", "connection"],
      "properties": {
        "outline": {"type": "array", "items": {"type": "string"}},
        "opening": {"$ref": "#/$defs/part"},
        "middle": {"$ref": "#/$defs/part"},
        "ending": {"$ref": "#/$defs/part"},
        "connection": {"type": "string"}
      }
    },
    "scores": {
      "type": "object",
      "additionalProperties": false,
      "required": ["pace", "pauses", "fillers", "clarity", "flow"],
      "properties": {
        "pace": {"$ref": "#/$defs/score"},
        "pauses": {"$ref": "#/$defs/score"},
        "fillers": {"$ref": "#/$defs/score"},
        "clarity": {"$ref": "#/$defs/score"},
        "flow": {"$ref": "#/$defs/score"}
      }
    },
    "moments": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["at", "category", "quote", "issue", "fix"],
        "properties": {
          "at": {"type": "number"},
          "category": {"type": "string", "enum": ["pace", "pauses", "fillers", "clarity", "flow"]},
          "quote": {"type": "string"},
          "issue": {"type": "string"},
          "fix": {"type": "string"}
        }
      }
    },
    "strengths": {"type": "array", "items": {"type": "string"}},
    "nextStep": {"type": "string"}
  },
  "$defs": {
    "part": {
      "type": "object",
      "additionalProperties": false,
      "required": ["rating", "note"],
      "properties": {
        "rating": {"type": "string", "enum": ["strong", "ok", "weak"]},
        "note": {"type": "string"}
      }
    },
    "score": {
      "type": "object",
      "additionalProperties": false,
      "required": ["score", "summary"],
      "properties": {
        "score": {"type": "integer"},
        "summary": {"type": "string"}
      }
    }
  }
}`)

func (c *Claude) Evaluate(ctx context.Context, in CoachInput) (Feedback, error) {
	language := "English"
	if in.Lang == "ar" {
		language = "Arabic (Modern Standard Arabic, simple and friendly)"
	}
	topic := in.Topic
	if topic == "" {
		topic = "(no topic given: the speaker chose freely)"
	}
	fillers := make([]string, 0, len(in.Metrics.Fillers))
	for w, n := range in.Metrics.Fillers {
		fillers = append(fillers, fmt.Sprintf("%s ×%d", w, n))
	}
	sort.Strings(fillers)
	user := fmt.Sprintf(`Topic: %s
Recording length: %.0f seconds
Words: %d
Words per minute: %d
Filler words: %d (%s)
Pauses longer than 1.5s: %d (longest %.1fs)
Words per minute in each 15-second stretch: %s

Transcript:
%s`, topic, in.DurationSeconds, in.Metrics.WordCount, in.Metrics.WordsPerMinute,
		in.Metrics.FillerCount, strings.Join(fillers, ", "), in.Metrics.LongPauses, in.Metrics.LongestPauseSeconds, paceLine(in.Metrics.PaceTimeline), in.Transcript)

	resp, err := c.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     c.model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: fmt.Sprintf(coachSystem, language)}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user)),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: feedbackSchema},
		},
		// If a safety classifier declines, Anthropic retries on its
		// recommended fallback model inside the same call.
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		return Feedback{}, fmt.Errorf("claude: %w", err)
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return Feedback{}, ErrDeclined
	}
	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	var fb Feedback
	if err := json.Unmarshal([]byte(text), &fb); err != nil {
		return Feedback{}, fmt.Errorf("claude: unreadable feedback (stop reason %s): %w", resp.StopReason, err)
	}
	return Clean(fb, in.DurationSeconds), nil
}

func paceLine(p []int) string {
	if len(p) == 0 {
		return "(not measured)"
	}
	parts := make([]string, len(p))
	for i, v := range p {
		parts[i] = fmt.Sprintf("%d:%02d %d", int(float64(i)*PaceWindow)/60, int(float64(i)*PaceWindow)%60, v)
	}
	return strings.Join(parts, ", ")
}

// Clean keeps the coach's output inside the documented limits.
func Clean(fb Feedback, duration float64) Feedback {
	clamp := func(n int) int { return min(10, max(1, n)) }
	fb.Overall = clamp(fb.Overall)
	for _, s := range []*Score{&fb.Scores.Pace, &fb.Scores.Pauses, &fb.Scores.Fillers, &fb.Scores.Clarity, &fb.Scores.Flow} {
		s.Score = clamp(s.Score)
	}
	sort.SliceStable(fb.Moments, func(i, j int) bool { return fb.Moments[i].At < fb.Moments[j].At })
	if len(fb.Moments) > 8 {
		fb.Moments = fb.Moments[:8]
	}
	for i := range fb.Moments {
		fb.Moments[i].At = max(0, fb.Moments[i].At)
		if duration > 0 {
			fb.Moments[i].At = min(fb.Moments[i].At, duration)
		}
	}
	if fb.Moments == nil {
		fb.Moments = []Moment{}
	}
	if len(fb.Strengths) > 3 {
		fb.Strengths = fb.Strengths[:3]
	}
	if fb.Strengths == nil {
		fb.Strengths = []string{}
	}
	if len(fb.Story.Outline) > 5 {
		fb.Story.Outline = fb.Story.Outline[:5]
	}
	if fb.Story.Outline == nil {
		fb.Story.Outline = []string{}
	}
	for _, p := range []*Part{&fb.Story.Opening, &fb.Story.Middle, &fb.Story.Ending} {
		if p.Rating != "strong" && p.Rating != "weak" {
			p.Rating = "ok"
		}
	}
	return fb
}
