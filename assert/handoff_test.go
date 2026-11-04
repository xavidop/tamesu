package assert

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

type handoffJudger struct {
	verdict JudgeVerdict
	err     error
	got     JudgeRequest
}

func (f *handoffJudger) Judge(_ context.Context, req JudgeRequest) (JudgeVerdict, error) {
	f.got = req
	return f.verdict, f.err
}

func handoffRun(detail string) *RunResult {
	r := &RunResult{Transcript: []Turn{{Role: "user", Text: "I want a person"}, {Role: "agent", Text: "Connecting you now", Index: 1}}}
	if detail != "" {
		r.Events = []target.Event{{Kind: EventHandoff, Detail: detail}}
	}
	return r
}

func TestHandoff(t *testing.T) {
	str := func(s string) *string { return &s }
	no := false
	detail := `{"turn":2,"to":"billing","summary":"Customer Marta wants a refund for ORD-77, shoes arrived damaged","via":"tool transfer_to_human"}`

	a, err := Handoff(HandoffCheck{})
	require.NoError(t, err)
	assert.Equal(t, "handoff", a.Name())
	res := a.Evaluate(context.Background(), handoffRun(detail))
	assert.True(t, res.Pass, res.Reason)
	assert.Contains(t, res.Reason, "handed off on turn 2 (to billing, via tool transfer_to_human)")
	assert.Equal(t, []string{detail}, res.Evidence)

	res = a.Evaluate(context.Background(), handoffRun(""))
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "never handed the conversation to a person")

	a, _ = Handoff(HandoffCheck{Expected: &no})
	assert.Equal(t, "handoff none", a.Name())
	assert.True(t, a.Evaluate(context.Background(), handoffRun("")).Pass)
	res = a.Evaluate(context.Background(), handoffRun(detail))
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "handed off on turn 2")
	assert.Contains(t, res.Reason, "no handoff was expected")

	a, _ = Handoff(HandoffCheck{WithinTurns: 1})
	res = a.Evaluate(context.Background(), handoffRun(detail))
	assert.False(t, res.Pass)
	assert.Equal(t, "the agent handed off on turn 2, want turn 1 or earlier", res.Reason)
	a, _ = Handoff(HandoffCheck{WithinTurns: 2, To: str("^bill"), SummaryContains: []string{"ORD-77", "damaged"}})
	assert.True(t, a.Evaluate(context.Background(), handoffRun(detail)).Pass)

	a, _ = Handoff(HandoffCheck{To: str("^sales$")})
	res = a.Evaluate(context.Background(), handoffRun(detail))
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, `went to "billing", which does not match "^sales$"`)

	a, _ = Handoff(HandoffCheck{SummaryContains: []string{"voucher"}})
	res = a.Evaluate(context.Background(), handoffRun(detail))
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, `summary does not contain "voucher"`)

	// A SIP transfer counts as a handoff to its target.
	call := &RunResult{Transcript: []Turn{{Role: "agent", Text: "One moment", Index: 1}}, Events: []target.Event{{Kind: EventTransfer, Detail: "sip:billing@pbx"}}}
	a, _ = Handoff(HandoffCheck{To: str("billing")})
	res = a.Evaluate(context.Background(), call)
	assert.True(t, res.Pass, res.Reason)
	assert.Contains(t, res.Reason, "turn 1 (to sip:billing@pbx, via transfer)")

	_, err = Handoff(HandoffCheck{To: str("(")})
	assert.Error(t, err)
}

func TestHandoffSummaryJudge(t *testing.T) {
	detail := `{"turn":1,"to":"support","summary":"Marta, ORD-77, refund for damaged shoes","via":"tool transfer_to_human"}`
	r := handoffRun(detail)
	a, _ := Handoff(HandoffCheck{Summary: "The summary names the order and the problem"})

	res := a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "no judge model")

	j := &handoffJudger{verdict: JudgeVerdict{Pass: true, Score: 1, Reason: "it names ORD-77 and the damage", Evidence: []string{"ORD-77"}, TraceID: "tr"}}
	r.Judger = j
	r.Language = "en"
	res = a.Evaluate(context.Background(), r)
	assert.True(t, res.Pass)
	assert.Equal(t, "summary: it names ORD-77 and the damage", res.Reason)
	assert.Equal(t, []string{detail, "ORD-77"}, res.Evidence)
	assert.Equal(t, "tr", res.TraceID)
	assert.Contains(t, j.got.Rubric, `"Marta, ORD-77, refund for damaged shoes"`)
	assert.Contains(t, j.got.Rubric, "The summary names the order and the problem")
	assert.Equal(t, "en", j.got.Language)
	assert.Len(t, j.got.Transcript, 2)

	j.err = errors.New("model down")
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "model down")

	r.Events = []target.Event{{Kind: EventHandoff, Detail: `{"turn":1,"to":"support"}`}}
	j.err = nil
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "carried no summary")
}
