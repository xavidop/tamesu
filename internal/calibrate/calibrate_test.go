package calibrate

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
)

const labelsYAML = `
- name: states the policy
  rubric: "The agent states the 30-day return policy"
  expected: pass
  transcript:
    - { role: user, text: "Can I return shoes from 40 days ago?" }
    - { role: assistant, text: "Our window is 30 days, so I'm afraid not." }
  tool_calls:
    - { name: lookup_policy, args: { topic: returns } }
- name: invents an exception
  rubric: "The agent states the 30-day return policy"
  expected: fail
  transcript:
    - { role: user, text: "Can I return shoes from 40 days ago?" }
    - { role: agent, text: "Sure, send them back any time." }
- name: warm tone
  rubric: "The tone stays warm"
  scale: 1-5
  min: 4
  expected: pass
  transcript:
    - { role: user, text: "Hi" }
    - { role: agent, text: "Hello! Lovely to hear from you." }
`

type keywordJudge struct{ err error }

// Judge passes when the last agent turn contains "30 days" or "Lovely".
func (j keywordJudge) Judge(_ context.Context, req tassert.JudgeRequest) (tassert.JudgeVerdict, error) {
	if j.err != nil {
		return tassert.JudgeVerdict{}, j.err
	}
	last := req.Transcript[len(req.Transcript)-1].Text
	pass := strings.Contains(last, "30 days") || strings.Contains(last, "Lovely")
	return tassert.JudgeVerdict{Pass: pass, Reason: "keyword " + last, Score: 4}, nil
}

func TestParse(t *testing.T) {
	labels, err := Parse([]byte(labelsYAML))
	require.NoError(t, err)
	require.Len(t, labels, 3)
	req := labels[0].Request()
	assert.Equal(t, "binary", req.Scale)
	assert.Equal(t, "agent", req.Transcript[1].Role, "assistant is normalised")
	assert.Equal(t, 1, req.Transcript[1].Index)
	assert.Equal(t, "lookup_policy", req.ToolCalls[0].Name)
	assert.Equal(t, "returns", req.ToolCalls[0].Args["topic"])
	assert.Equal(t, "1-5", labels[2].Request().Scale)
	assert.Equal(t, 4.0, labels[2].Request().Min)

	wrapped, err := Parse([]byte("labels:\n  - { rubric: r, expected: yes, transcript: [{ role: user, text: x }] }\n"))
	require.NoError(t, err)
	assert.Equal(t, "pass", wrapped[0].Expected)
	assert.Equal(t, "label 1", wrapped[0].Name)

	for _, bad := range []string{
		"- { rubric: r, expected: maybe, transcript: [{ role: user, text: x }] }",
		"- { rubric: r, expected: pass }",
		"- { expected: pass, transcript: [{ role: user, text: x }] }",
		"- { rubric: r, expected: pass, transcript: [{ role: narrator, text: x }] }",
		"[]",
		"just text",
	} {
		_, err := Parse([]byte(bad))
		assert.Error(t, err, bad)
	}
}

func TestRunAndPrint(t *testing.T) {
	labels, err := Parse([]byte(labelsYAML))
	require.NoError(t, err)
	// Make the judge wrong on the warm tone label: no keyword.
	labels[2].Transcript[1].Text = "Hello."
	rep := Run(context.Background(), keywordJudge{}, labels, 2)
	assert.Equal(t, 3, rep.Labels)
	assert.Equal(t, 2, rep.Agree)
	assert.Equal(t, 0, rep.FalsePass)
	assert.Equal(t, 1, rep.FalseFail)
	assert.InDelta(t, 2.0/3, rep.Agreement, 1e-9)
	require.Len(t, rep.Rubrics, 2)
	assert.Equal(t, "The tone stays warm", rep.Rubrics[0].Rubric, "the weakest rubric first")
	assert.Equal(t, 0.0, rep.Rubrics[0].Agreement)
	assert.Equal(t, 1.0, rep.Rubrics[1].Agreement)
	assert.Equal(t, "fail", rep.Results[2].Got)
	assert.Equal(t, 4.0, rep.Results[2].Score)

	var buf bytes.Buffer
	rep.Print(&buf, 0.8)
	out := buf.String()
	assert.Contains(t, out, "The agent states the 30-day return policy  2       2      0           0           0       100%")
	assert.Contains(t, out, "agreement 67% (2 of 3 labels), 0 false pass, 1 false fail, 0 errors · minimum 80%: BELOW THE MINIMUM")
	assert.Contains(t, out, "x warm tone: expected pass, judge said fail · keyword Hello.")

	broken := Run(context.Background(), keywordJudge{err: errors.New("quota")}, labels[:1], 1)
	assert.Equal(t, 1, broken.Errors)
	assert.Equal(t, "error", broken.Results[0].Got)
	assert.Equal(t, 0.0, broken.Agreement)
	buf.Reset()
	broken.Print(&buf, 0.5)
	assert.Contains(t, buf.String(), "judge error: quota")
}
