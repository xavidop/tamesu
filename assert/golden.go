package assert

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// Golden modes.
const (
	GoldenExact   = "exact"
	GoldenSimilar = "similar"
	GoldenJudge   = "judge"
)

// DefaultGoldenMin is the word overlap a similar golden turn needs.
const DefaultGoldenMin = 0.8

// Golden compares the agent's turns, in order, with reference turns: exact
// (normalised text), similar (word overlap of at least min) or judge (the
// judge decides whether each turn means the same as its reference). Fewer
// agent turns than references fail; extra agent turns are ignored.
func Golden(reference []string, mode string, min float64, source string) Assertion {
	if mode == "" {
		mode = GoldenSimilar
	}
	if min <= 0 {
		min = DefaultGoldenMin
	}
	label := "golden " + mode
	if source != "" {
		label += " " + source
	}
	return Func(label, func(ctx context.Context, r *RunResult) Result {
		var agent []string
		for _, t := range r.Transcript {
			if t.Role == "agent" {
				agent = append(agent, t.Text)
			}
		}
		if len(reference) == 0 {
			return Result{Pass: false, Reason: "the reference has no turns"}
		}
		if len(agent) < len(reference) {
			return Result{Pass: false, Reason: fmt.Sprintf("the agent produced %d turns, the reference has %d", len(agent), len(reference))}
		}
		if mode == GoldenJudge {
			return goldenJudge(ctx, r, reference)
		}
		for i, ref := range reference {
			got := agent[i]
			switch mode {
			case GoldenExact:
				if normalizeText(got) != normalizeText(ref) {
					return Result{Pass: false, Reason: fmt.Sprintf("turn %d differs from the reference", i+1), Evidence: []string{"got: " + got, "want: " + ref}}
				}
			default:
				if s := Similarity(got, ref); s < min {
					return Result{Pass: false, Reason: fmt.Sprintf("turn %d is %.0f%% similar to the reference, want %.0f%%", i+1, s*100, min*100), Evidence: []string{"got: " + got, "want: " + ref}}
				}
			}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("all %d turns match the reference (%s)", len(reference), mode)}
	})
}

func goldenJudge(ctx context.Context, r *RunResult, reference []string) Result {
	if r.Judger == nil {
		return Result{Pass: false, Reason: "no judge model is available in this run"}
	}
	var b strings.Builder
	b.WriteString("Each agent turn must mean the same as its reference answer below, allowing different wording. Fail if any turn contradicts, omits or adds a material fact.\n")
	for i, ref := range reference {
		fmt.Fprintf(&b, "Reference for agent turn %d: %s\n", i+1, ref)
	}
	v, err := r.Judger.Judge(ctx, JudgeRequest{Rubric: b.String(), Scale: "binary", Transcript: r.Transcript, ToolCalls: r.ToolCalls, Metrics: r.Metrics})
	if err != nil {
		return Result{Pass: false, Reason: "judge failed: " + err.Error()}
	}
	return Result{Pass: v.Pass, Reason: v.Reason, Evidence: v.Evidence, TraceID: v.TraceID}
}

// normalizeText lowercases, drops punctuation and squeezes whitespace.
func normalizeText(s string) string {
	return strings.Join(words(s), " ")
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// Similarity is the Jaccard overlap of the two texts' word sets, 0 to 1.
func Similarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 && len(wb) == 0 {
		return 1
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	inter, union := 0, len(set)
	seen := map[string]bool{}
	for _, w := range wb {
		if seen[w] {
			continue
		}
		seen[w] = true
		if set[w] {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}
