package speech

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
)

// LongPause is the gap between words, in seconds, that counts as a long pause.
const LongPause = 1.5

// Token is one transcript word as the website shows it.
type Token struct {
	W string  `json:"w"`
	S float64 `json:"s"`
	E float64 `json:"e"`
	F bool    `json:"f,omitempty"` // filler word
}

type Pause struct {
	At      float64 `json:"at"`
	Seconds float64 `json:"seconds"`
}

type Metrics struct {
	WordsPerMinute      int            `json:"wordsPerMinute"`
	WordCount           int            `json:"wordCount"`
	FillerCount         int            `json:"fillerCount"`
	Fillers             map[string]int `json:"fillers"`
	LongPauses          int            `json:"longPauses"`
	LongestPauseSeconds float64        `json:"longestPauseSeconds"`
	// PaceTimeline is words per minute in each 30-second stretch, so the
	// report can show where they sped up or slowed down.
	PaceTimeline []int   `json:"paceTimeline"`
	Pauses       []Pause `json:"-"` // start times, for the prompt
}

var (
	fillersEN = map[string]bool{
		"um": true, "umm": true, "uh": true, "uhh": true, "er": true, "erm": true, "ah": true,
		"hmm": true, "hm": true, "mm": true, "uhm": true, "eh": true, "like": true, "basically": true, "actually": true, "literally": true,
	}
	phrasesEN = []string{"you know", "i mean", "sort of", "kind of"}
	fillersAR = map[string]bool{
		"يعني": true, "امم": true, "اه": true, "آه": true, "آآ": true, "ااا": true,
		"طيب": true, "بس": true, "خلاص": true, "ممم": true, "اممم": true, "إمم": true,
	}
	// Drawn-out hesitation sounds such as "اااا" or "مممم".
	elongatedAR = regexp.MustCompile(`^[اآأإمهـ]{2,}$`)
	elongatedEN = regexp.MustCompile(`^(u+h+|u+m+|e+r+m*|a+h+|h+m+|m{2,})$`)
)

func normalize(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r) || unicode.IsSymbol(r)
	}))
}

func isFiller(w, lang string) bool {
	if lang == "ar" {
		return fillersAR[w] || elongatedAR.MatchString(w)
	}
	return fillersEN[w] || elongatedEN.MatchString(w)
}

// Analyze measures the recording and marks filler words in the transcript.
func Analyze(words []Word, lang string) ([]Token, Metrics) {
	tokens := make([]Token, len(words))
	m := Metrics{Fillers: map[string]int{}}
	norm := make([]string, len(words))
	for i, w := range words {
		tokens[i] = Token{W: w.Text, S: round2(w.Start), E: round2(w.End)}
		norm[i] = normalize(w.Text)
	}
	// Two-word phrases first, so "you know" isn't also counted word by word.
	if lang != "ar" {
		for i := 0; i+1 < len(norm); i++ {
			pair := norm[i] + " " + norm[i+1]
			for _, p := range phrasesEN {
				if pair == p && !tokens[i].F && !tokens[i+1].F {
					tokens[i].F, tokens[i+1].F = true, true
					m.Fillers[p]++
				}
			}
		}
	}
	for i, w := range norm {
		if w == "" || tokens[i].F {
			continue
		}
		if isFiller(w, lang) {
			tokens[i].F = true
			key := w
			if lang == "ar" && elongatedAR.MatchString(w) && !fillersAR[w] {
				key = "ااا"
			} else if lang != "ar" && elongatedEN.MatchString(w) && !fillersEN[w] {
				key = canonicalEN(w)
			}
			m.Fillers[key]++
		}
	}
	for _, n := range m.Fillers {
		m.FillerCount += n
	}
	for _, w := range norm {
		if w != "" {
			m.WordCount++
		}
	}
	for i := 1; i < len(words); i++ {
		gap := words[i].Start - words[i-1].End
		if gap > LongPause {
			m.LongPauses++
			m.Pauses = append(m.Pauses, Pause{At: round2(words[i-1].End), Seconds: round1(gap)})
			if gap > m.LongestPauseSeconds {
				m.LongestPauseSeconds = round1(gap)
			}
		}
	}
	if len(words) > 0 {
		span := words[len(words)-1].End - words[0].Start
		if span > 0 {
			m.WordsPerMinute = int(math.Round(float64(m.WordCount) / (span / 60)))
		}
	}
	m.PaceTimeline = paceTimeline(words, norm)
	return tokens, m
}

// PaceWindow is the length, in seconds, of each stretch in the pace timeline:
// one block for a 30-second talk, two for a minute.
const PaceWindow = 30.0

// paceTimeline counts the words started in each 30-second window and turns
// them into words per minute. A short last window is scaled to its length,
// and dropped when it is under 5 seconds.
func paceTimeline(words []Word, norm []string) []int {
	if len(words) == 0 {
		return []int{}
	}
	start, end := words[0].Start, words[len(words)-1].End
	n := int(math.Ceil((end - start) / PaceWindow))
	if n < 1 {
		n = 1
	}
	counts := make([]int, n)
	for i, w := range words {
		if norm[i] == "" {
			continue
		}
		k := min(int((w.Start-start)/PaceWindow), n-1)
		counts[k]++
	}
	out := make([]int, 0, n)
	for k, c := range counts {
		length := PaceWindow
		if k == n-1 {
			length = end - start - PaceWindow*float64(n-1)
			if length < 8 && n > 1 {
				break
			}
		}
		if length <= 0 {
			length = PaceWindow
		}
		out = append(out, int(math.Round(float64(c)/(length/60))))
	}
	return out
}

// PromptTranscript renders the transcript for the coach: a [m:ss] mark at the
// start of each sentence, {braces} around filler words, and long pauses
// inline as [pause 2.4s].
func PromptTranscript(tokens []Token) string {
	var b strings.Builder
	newSentence := true
	for i, t := range tokens {
		if i > 0 {
			if gap := t.S - tokens[i-1].E; gap > LongPause {
				fmt.Fprintf(&b, " [pause %.1fs]", gap)
			}
		}
		if newSentence {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[%d:%02d] ", int(t.S)/60, int(t.S)%60)
			newSentence = false
		} else {
			b.WriteString(" ")
		}
		if t.F {
			b.WriteString("{" + t.W + "}")
		} else {
			b.WriteString(t.W)
		}
		if strings.ContainsAny(t.W, ".?!؟") {
			newSentence = true
		}
	}
	return b.String()
}

// canonicalEN folds drawn-out sounds ("ummm", "uhhh") into one key.
func canonicalEN(w string) string {
	switch {
	case strings.HasPrefix(w, "u") && strings.Contains(w, "m"):
		return "um"
	case strings.HasPrefix(w, "u"):
		return "uh"
	case strings.HasPrefix(w, "e"):
		return "er"
	case strings.HasPrefix(w, "a"):
		return "ah"
	}
	return "hmm"
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round2(f float64) float64 { return math.Round(f*100) / 100 }
