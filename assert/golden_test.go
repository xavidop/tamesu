package assert

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGolden(t *testing.T) {
	r := sampleRun()
	ref := []string{"Our return window is 30 days so unfortunately not", "I'm sorry the 30-day policy has no exceptions. Order #123456 is closed"}
	res := Golden(ref, GoldenExact, 0, "golden/refund.json").Evaluate(context.Background(), r)
	assert.True(t, res.Pass, res.Reason)
	assert.Equal(t, "golden exact golden/refund.json", res.Name)
	res = Golden([]string{"Our return window is 30 days", "something else entirely different here"}, GoldenExact, 0, "").Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Equal(t, "turn 1 differs from the reference", res.Reason)
	res = Golden([]string{"The return window is 30 days, so unfortunately not."}, "", 0, "").Evaluate(context.Background(), r)
	assert.True(t, res.Pass, res.Reason)
	res = Golden([]string{"We ship worldwide in two days"}, GoldenSimilar, 0.5, "").Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "turn 1 is")
	res = Golden([]string{"a", "b", "c"}, GoldenExact, 0, "").Evaluate(context.Background(), r)
	assert.Equal(t, "the agent produced 2 turns, the reference has 3", res.Reason)
	assert.Equal(t, "the reference has no turns", Golden(nil, GoldenExact, 0, "").Evaluate(context.Background(), r).Reason)

	j := &fakeJudger{verdict: JudgeVerdict{Pass: true, Reason: "same meaning", TraceID: "t9"}}
	r.Judger = j
	res = Golden([]string{"no returns after 30 days", "no exceptions"}, GoldenJudge, 0, "").Evaluate(context.Background(), r)
	assert.True(t, res.Pass)
	assert.Equal(t, "t9", res.TraceID)
	assert.Contains(t, j.got.Rubric, "Reference for agent turn 2: no exceptions")
	r.Judger = nil
	assert.Equal(t, "no judge model is available in this run", Golden([]string{"x", "y"}, GoldenJudge, 0, "").Evaluate(context.Background(), r).Reason)
}

func TestSimilarity(t *testing.T) {
	assert.Equal(t, 1.0, Similarity("Hello, World!", "hello world"))
	assert.Equal(t, 0.0, Similarity("red", "blue"))
	assert.InDelta(t, 0.5, Similarity("a b c d", "a b"), 0.001)
	assert.Equal(t, 1.0, Similarity("", ""))
}
