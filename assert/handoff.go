package assert

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// EventHandoff is recorded when the agent handed the conversation to a
// person: Detail is a HandoffDetail as JSON.
const EventHandoff = "handoff"

// HandoffDetail is what a handoff event carries.
type HandoffDetail struct {
	// Turn is the agent turn (1-based) that handed off.
	Turn int `json:"turn"`
	// To is the destination (a department, a queue, a SIP target) when the
	// signal names one.
	To string `json:"to,omitempty"`
	// Summary is what the agent passed on to the person, when the signal
	// names one.
	Summary string `json:"summary,omitempty"`
	// Via says what signalled it: "tool transfer_to_human", "json_path
	// $.handoff", "regex", "transfer".
	Via string `json:"via,omitempty"`
}

// HandoffCheck is a handoff and escalation assertion: whether the agent
// handed the conversation to a person, when, to whom and with what summary.
type HandoffCheck struct {
	// Expected is whether a handoff should have happened (nil means yes).
	Expected *bool
	// WithinTurns, when positive, is the latest agent turn the handoff may
	// happen on.
	WithinTurns int
	// To is a regular expression the destination must match.
	To *string
	// SummaryContains are texts the summary must contain.
	SummaryContains []string
	// Summary is a judge rubric for the summary (needs a judge).
	Summary string
}

// Handoff builds the assertion for a HandoffCheck. It reads the handoff
// events the run recorded (a target's handoff signal) and the transfer
// events of phone calls (a SIP REFER counts as a handoff to its target).
func Handoff(c HandoffCheck) (Assertion, error) {
	var toRe *regexp.Regexp
	if c.To != nil {
		var err error
		if toRe, err = regexp.Compile(*c.To); err != nil {
			return nil, fmt.Errorf("handoff to %q: %w", *c.To, err)
		}
	}
	expected := c.Expected == nil || *c.Expected
	name := "handoff"
	if !expected {
		name = "handoff none"
	}
	return Func(name, func(ctx context.Context, r *RunResult) Result {
		handoffs := Handoffs(r)
		if !expected {
			if len(handoffs) == 0 {
				return Result{Pass: true, Reason: "the agent did not hand the conversation to a person"}
			}
			h := handoffs[0]
			return Result{Pass: false, Reason: fmt.Sprintf("the agent handed off on turn %d (%s), but no handoff was expected", h.Turn, describeHandoff(h)), Evidence: []string{handoffJSON(h)}}
		}
		if len(handoffs) == 0 {
			return Result{Pass: false, Reason: "the agent never handed the conversation to a person (no handoff or transfer event)"}
		}
		h := handoffs[0]
		evidence := []string{handoffJSON(h)}
		if c.WithinTurns > 0 && h.Turn > c.WithinTurns {
			return Result{Pass: false, Reason: fmt.Sprintf("the agent handed off on turn %d, want turn %d or earlier", h.Turn, c.WithinTurns), Evidence: evidence}
		}
		if toRe != nil && !toRe.MatchString(h.To) {
			return Result{Pass: false, Reason: fmt.Sprintf("the handoff went to %s, which does not match %s", quoteOr(h.To, "no destination"), quote(*c.To)), Evidence: evidence}
		}
		for _, want := range c.SummaryContains {
			if !strings.Contains(h.Summary, want) {
				return Result{Pass: false, Reason: fmt.Sprintf("the handoff summary does not contain %s", quote(want)), Evidence: evidence}
			}
		}
		if c.Summary != "" {
			if r.Judger == nil {
				return Result{Pass: false, Reason: "no judge model is configured for the summary rubric", Evidence: evidence}
			}
			if h.Summary == "" {
				return Result{Pass: false, Reason: "the handoff carried no summary to judge", Evidence: evidence}
			}
			req := JudgeRequest{
				Rubric:     fmt.Sprintf("The agent handed the conversation to a person with this summary: %q. Judge the summary against the conversation: %s", h.Summary, c.Summary),
				Language:   r.Language,
				Transcript: r.Transcript,
				ToolCalls:  r.ToolCalls,
			}
			v, err := r.Judger.Judge(ctx, req)
			if err != nil {
				return Result{Pass: false, Reason: "judging the summary: " + err.Error(), Evidence: evidence}
			}
			return Result{Pass: v.Pass, Score: v.Score, Reason: "summary: " + v.Reason, Evidence: append(evidence, v.Evidence...), TraceID: v.TraceID}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("the agent handed off on turn %d (%s)", h.Turn, describeHandoff(h)), Evidence: evidence}
	}), nil
}

// Handoffs lists the run's handoffs: handoff events, then phone transfers.
func Handoffs(r *RunResult) []HandoffDetail {
	var out []HandoffDetail
	for _, e := range r.Events {
		switch e.Kind {
		case EventHandoff:
			var h HandoffDetail
			if err := json.Unmarshal([]byte(e.Detail), &h); err != nil {
				h = HandoffDetail{Summary: e.Detail}
			}
			out = append(out, h)
		case EventTransfer:
			out = append(out, HandoffDetail{Turn: r.AgentTurns(), To: e.Detail, Via: "transfer"})
		}
	}
	return out
}

func describeHandoff(h HandoffDetail) string {
	parts := []string{}
	if h.To != "" {
		parts = append(parts, "to "+h.To)
	}
	if h.Via != "" {
		parts = append(parts, "via "+h.Via)
	}
	if len(parts) == 0 {
		return "no destination recorded"
	}
	return strings.Join(parts, ", ")
}

func handoffJSON(h HandoffDetail) string {
	b, _ := json.Marshal(h)
	return string(b)
}

func quoteOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return quote(s)
}
