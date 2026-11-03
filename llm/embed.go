package llm

import (
	"context"
	"fmt"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/assert"
)

// Embed implements assert.Embedder: one embedding call for the texts of a
// similar_to assertion, with the configured embedding model, its fallbacks
// and the retries every model call gets. The call is counted and priced like
// the others.
func (c *Client) Embed(ctx context.Context, req assert.EmbedRequest) (assert.EmbedResponse, error) {
	if len(req.Texts) == 0 {
		return assert.EmbedResponse{}, nil
	}
	if c.overBudget() {
		return assert.EmbedResponse{}, ErrBudgetExceeded
	}
	primary := req.Model
	if primary == "" {
		primary = c.embedderModel()
	}
	if primary == "" {
		return assert.EmbedResponse{}, fmt.Errorf("llm: no embedding model: set models.embedder (such as googleai/text-embedding-004 or openai/text-embedding-3-small)")
	}
	chars := 0
	for _, t := range req.Texts {
		chars += len(t)
	}
	return attempt(ctx, c, "embedder", chain(primary, c.fallbacksFor("embedder")), func(ctx context.Context, modelName string) (assert.EmbedResponse, error) {
		resp, err := genkit.Embed(ctx, c.g, ai.WithEmbedderName(modelName), ai.WithTextDocs(req.Texts...))
		if err != nil {
			return assert.EmbedResponse{}, fmt.Errorf("llm: embed with %s: %w", modelName, err)
		}
		if len(resp.Embeddings) != len(req.Texts) {
			return assert.EmbedResponse{}, fmt.Errorf("llm: embed with %s: %w: %d vectors for %d texts", modelName, ErrBadOutput, len(resp.Embeddings), len(req.Texts))
		}
		out := assert.EmbedResponse{Model: modelName, Vectors: make([][]float32, len(resp.Embeddings))}
		for i, e := range resp.Embeddings {
			out.Vectors[i] = e.Embedding
		}
		return out, c.recordEmbedding(modelName, chars)
	})
}

// embedderModel is models.embedder, or the usual embedder of the default
// model's provider when none is configured; "" for a provider without one.
func (c *Client) embedderModel() string {
	if m := c.cfg.Models.Embedder; m != "" {
		return m
	}
	return DefaultEmbedder(providerOf(c.cfg.Models.Default))
}

// DefaultEmbedder is the embedding model tamesu picks for a provider when
// models.embedder is not set: Gemini's text-embedding-004, OpenAI's
// text-embedding-3-small, fake/embedding; "" for the providers without one
// (Anthropic has no embeddings; Ollama's must be named).
func DefaultEmbedder(provider string) string {
	switch provider {
	case "googleai":
		return "googleai/text-embedding-004"
	case "vertexai":
		return "vertexai/text-embedding-004"
	case "openai":
		return "openai/text-embedding-3-small"
	case "fake":
		return "fake/embedding"
	}
	return ""
}
