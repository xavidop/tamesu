package assert

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubEmbedder answers with a fixed vector per text; unknown texts get a
// vector of their own that matches nothing.
type stubEmbedder struct {
	vectors map[string][]float32
	err     error
	reqs    []EmbedRequest
}

func (s *stubEmbedder) Embed(_ context.Context, req EmbedRequest) (EmbedResponse, error) {
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return EmbedResponse{}, s.err
	}
	out := EmbedResponse{Model: "stub/embedding"}
	for i, t := range req.Texts {
		v, ok := s.vectors[t]
		if !ok {
			v = make([]float32, 4)
			v[i%4] = 1
		}
		out.Vectors = append(out.Vectors, v)
	}
	return out, nil
}

func TestCosine(t *testing.T) {
	assert.InDelta(t, 1, Cosine([]float32{1, 2, 3}, []float32{2, 4, 6}), 1e-9, "the same direction")
	assert.InDelta(t, 0, Cosine([]float32{1, 0}, []float32{0, 1}), 1e-9, "orthogonal")
	assert.InDelta(t, -1, Cosine([]float32{1, 0}, []float32{-1, 0}), 1e-9)
	assert.Equal(t, 0.0, Cosine([]float32{1, 0}, []float32{1}), "different lengths")
	assert.Equal(t, 0.0, Cosine([]float32{0, 0}, []float32{1, 0}), "no magnitude")
	assert.Equal(t, 0.0, Cosine(nil, nil))
}

func TestSimilarTo(t *testing.T) {
	policy := "Returns are accepted within 30 days of delivery"
	emb := &stubEmbedder{vectors: map[string][]float32{
		policy:                        {1, 0.1, 0, 0},
		"You have 30 days to return.": {0.95, 0.2, 0, 0},
		"The weather is nice today.":  {0, 0, 1, 0},
	}}
	r := &RunResult{Embedder: emb, Transcript: []Turn{
		{Role: "user", Text: "Can I return these?"},
		{Role: "agent", Text: "The weather is nice today.", Index: 1},
		{Role: "agent", Text: "You have 30 days to return.", Index: 2},
	}}

	res := SimilarTo(policy, 0, "").Evaluate(context.Background(), r)
	assert.True(t, res.Pass, res.Reason)
	assert.Greater(t, res.Score, DefaultSimilarity)
	assert.Contains(t, res.Reason, "agent turn 2 is 0.9")
	assert.Equal(t, []string{"You have 30 days to return."}, res.Evidence)
	require.Len(t, emb.reqs, 1)
	assert.Equal(t, []string{policy, "The weather is nice today.", "You have 30 days to return."}, emb.reqs[0].Texts, "the reference first, then every agent turn, in one call")

	res = SimilarTo(policy, 0.999, "openai/text-embedding-3-large").Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "no agent turn is 1.00 similar")
	assert.Contains(t, res.Reason, "the closest, turn 2, scores 0.9")
	assert.Equal(t, "openai/text-embedding-3-large", emb.reqs[1].Model, "the assertion's model travels with the request")

	res = SimilarTo(policy, 0.5, "").Evaluate(context.Background(), &RunResult{Embedder: emb, Transcript: []Turn{{Role: "user", Text: "hi"}}})
	assert.False(t, res.Pass)
	assert.Equal(t, "the agent said nothing", res.Reason)

	res = SimilarTo(policy, 0.5, "").Evaluate(context.Background(), &RunResult{Transcript: r.Transcript})
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "no embedding model is available")

	res = SimilarTo(policy, 0.5, "").Evaluate(context.Background(), &RunResult{Embedder: &stubEmbedder{err: errors.New("quota")}, Transcript: r.Transcript})
	assert.False(t, res.Pass)
	assert.Equal(t, "embedding failed: quota", res.Reason)
	assert.Equal(t, "similar_to \"Returns are accepted within 30 days of delivery\"", SimilarTo(policy, 0, "").Name())
}
