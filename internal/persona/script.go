package persona

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

// DefaultScriptTimeout bounds one wait_for step.
const DefaultScriptTimeout = 30 * time.Second

// ScriptError is a scripted step that did not play out: a wait_for text the
// agent never said in time. It is a test failure, not an infrastructure error.
type ScriptError struct {
	Step   int
	Kind   string
	Text   string
	Reason string
}

func (e *ScriptError) Error() string {
	return fmt.Sprintf("script step %d (%s %q): %s", e.Step+1, e.Kind, e.Text, e.Reason)
}

// runScript plays the scenario's deterministic steps before the persona takes
// over: say sends text, dtmf sends digits and wait_for listens (empty turns)
// until the agent's transcript fuzzily contains the text.
func (l *Loop) runScript(ctx context.Context, sess target.Session, out *Outcome, now func() time.Time) error {
	timeout := l.ScriptTimeout
	if timeout <= 0 {
		timeout = DefaultScriptTimeout
	}
	for i, st := range l.Scenario.Script {
		switch st.Kind() {
		case "say", "dtmf":
			say := st.Say
			if l.Render != nil && say != "" {
				if r, err := l.Render(say); err == nil {
					say = r
				}
			}
			ut := target.UserTurn{Text: say, DTMF: st.DTMF, At: now()}
			if _, err := l.exchange(ctx, sess, out, ut, "script"); err != nil {
				return err
			}
			if out.StopReason != "" {
				return nil
			}
		case "wait_for":
			deadline := now().Add(timeout)
			heard := transcriptText(out.Transcript, "agent")
			for !fuzzyContains(heard, st.WaitFor) {
				if remaining := deadline.Sub(now()); remaining <= 0 {
					return &ScriptError{Step: i, Kind: "wait_for", Text: st.WaitFor, Reason: fmt.Sprintf("not heard within %s (heard: %s)", timeout, short(heard))}
				}
				wctx, cancel := context.WithDeadline(ctx, deadline)
				agent, err := l.exchange(wctx, sess, out, target.UserTurn{At: now()}, "wait_for")
				cancel()
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
						return &ScriptError{Step: i, Kind: "wait_for", Text: st.WaitFor, Reason: fmt.Sprintf("not heard within %s (heard: %s)", timeout, short(heard))}
					}
					return err
				}
				heard += " " + agent.Text
				if out.StopReason != "" {
					if fuzzyContains(heard, st.WaitFor) {
						return nil
					}
					return &ScriptError{Step: i, Kind: "wait_for", Text: st.WaitFor, Reason: "the conversation ended first (" + out.StopReason + ")"}
				}
			}
		default:
			return &ScriptError{Step: i, Kind: "?", Reason: "a script step is exactly one of wait_for, dtmf, say"}
		}
	}
	return nil
}

// exchange sends one user turn and records the agent's answer; a listening
// turn (no text, no digits) adds no user line to the transcript.
func (l *Loop) exchange(ctx context.Context, sess target.Session, out *Outcome, ut target.UserTurn, kind string) (target.AgentTurn, error) {
	if ut.Text != "" || ut.DTMF != "" {
		text := ut.Text
		if ut.DTMF != "" {
			text = strings.TrimSpace(text + " [dtmf " + ut.DTMF + "]")
		}
		userTurn := assert.Turn{Role: "user", Text: text, At: ut.At}
		out.Transcript = append(out.Transcript, userTurn)
		l.notify(userTurn)
	}
	if err := sess.Send(ctx, ut); err != nil {
		return target.AgentTurn{}, fmt.Errorf("%s: send: %w", kind, err)
	}
	agent, err := sess.Receive(ctx)
	if err != nil {
		return target.AgentTurn{}, fmt.Errorf("%s: receive: %w", kind, err)
	}
	endedAt := agent.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	if agent.Text != "" || agent.Done {
		idx := 0
		for _, t := range out.Transcript {
			if t.Role == "agent" {
				idx++
			}
		}
		agentTurn := assert.Turn{Role: "agent", Text: agent.Text, At: endedAt, Index: idx + 1}
		out.Transcript = append(out.Transcript, agentTurn)
		l.notify(agentTurn)
	}
	out.AgentTurns = append(out.AgentTurns, agent)
	if l.OnAgentTurn != nil {
		l.OnAgentTurn(agent)
	}
	if !ut.At.IsZero() {
		out.LatenciesMs = append(out.LatenciesMs, ms(endedAt.Sub(ut.At)))
	}
	if l.Handoff != nil {
		if detail := l.Handoff(agent, len(out.AgentTurns)); detail != "" {
			out.Events = append(out.Events, target.Event{At: endedAt, Kind: assert.EventHandoff, Detail: detail})
			out.StopReason = StopHandoff
			return agent, nil
		}
	}
	if agent.Done {
		out.StopReason = StopAgentDone
	}
	return agent, nil
}

func transcriptText(turns []assert.Turn, role string) string {
	var b strings.Builder
	for _, t := range turns {
		if t.Role == role {
			b.WriteString(t.Text)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// fuzzyContains reports whether want's words appear in heard (normalised:
// lower case, letters and digits only), allowing one in five words to differ
// (an STT slip), and always requiring the first and last words.
func fuzzyContains(heard, want string) bool {
	h := normalizeWords(heard)
	w := normalizeWords(want)
	if len(w) == 0 {
		return true
	}
	if len(h) < len(w) {
		return false
	}
	allowed := len(w) / 5
	for start := 0; start+len(w) <= len(h); start++ {
		misses := 0
		for j := range w {
			if h[start+j] != w[j] {
				misses++
			}
		}
		if misses <= allowed {
			return true
		}
	}
	// Numbers spoken as words or digits ("two" vs "2") are a common slip:
	// retry with digits spelled out on both sides.
	if strings.Join(h, " ") != spellDigits(strings.Join(h, " ")) || strings.Join(w, " ") != spellDigits(strings.Join(w, " ")) {
		return fuzzyContains(spellDigits(strings.Join(h, " ")), spellDigits(strings.Join(w, " ")))
	}
	return false
}

func normalizeWords(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Fields(b.String())
}

var digitWords = map[string]string{"0": "zero", "1": "one", "2": "two", "3": "three", "4": "four", "5": "five", "6": "six", "7": "seven", "8": "eight", "9": "nine"}

func spellDigits(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if d, ok := digitWords[w]; ok {
			words[i] = d
		}
	}
	return strings.Join(words, " ")
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return "..." + s[len(s)-157:]
	}
	if s == "" {
		return "nothing"
	}
	return s
}

// scriptSteps is a guard for an empty scenario.
func scriptSteps(sc config.Scenario) int { return len(sc.Script) }
