package assert

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// EmbedRequest asks for the embedding of each text, with one model.
type EmbedRequest struct {
	// Model overrides the configured embedding model (a Genkit reference).
	Model string   `json:"model,omitempty"`
	Texts []string `json:"texts"`
}

// EmbedResponse is one vector per text, in order, and the model that made them.
type EmbedResponse struct {
	Vectors [][]float32 `json:"vectors"`
	Model   string      `json:"model,omitempty"`
}

// Embedder turns texts into vectors for the semantic assertions. The llm
// module implements it; the runner sets it on every RunResult.
type Embedder interface {
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
}

// DefaultSimilarity is the cosine similarity similar_to needs when the
// assertion gives no min.
const DefaultSimilarity = 0.8

// SimilarTo passes when some agent turn means the same as text: the cosine
// similarity of their embeddings is at least min (DefaultSimilarity when 0).
// model picks the embedding model; "" uses the configured one.
func SimilarTo(text string, min float64, model string) Assertion {
	if min <= 0 {
		min = DefaultSimilarity
	}
	return Func("similar_to "+quote(text), func(ctx context.Context, r *RunResult) Result {
		if r.Embedder == nil {
			return Result{Pass: false, Reason: "no embedding model is available in this run (set models.embedder)"}
		}
		var turns []Turn
		texts := []string{text}
		for _, t := range r.Transcript {
			if t.Role == "agent" && strings.TrimSpace(t.Text) != "" {
				turns = append(turns, t)
				texts = append(texts, t.Text)
			}
		}
		if len(turns) == 0 {
			return Result{Pass: false, Reason: "the agent said nothing"}
		}
		resp, err := r.Embedder.Embed(ctx, EmbedRequest{Model: model, Texts: texts})
		if err != nil {
			return Result{Pass: false, Reason: "embedding failed: " + err.Error()}
		}
		if len(resp.Vectors) != len(texts) {
			return Result{Pass: false, Reason: fmt.Sprintf("embedding failed: %d vectors for %d texts", len(resp.Vectors), len(texts))}
		}
		best, bestIdx := -1.0, -1
		for i := range turns {
			sim := Cosine(resp.Vectors[0], resp.Vectors[i+1])
			if sim > best {
				best, bestIdx = sim, i
			}
		}
		turn := turns[bestIdx]
		if best >= min {
			return Result{Pass: true, Score: best, Reason: fmt.Sprintf("agent turn %d is %.2f similar to %s (min %.2f)", turn.Index, best, quote(text), min), Evidence: []string{turn.Text}}
		}
		return Result{Pass: false, Score: best, Reason: fmt.Sprintf("no agent turn is %.2f similar to %s; the closest, turn %d, scores %.2f", min, quote(text), turn.Index, best), Evidence: []string{turn.Text}}
	})
}

// Cosine is the cosine similarity of two vectors: 1 for the same direction,
// 0 for unrelated ones. Vectors of different lengths or without magnitude
// score 0.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
