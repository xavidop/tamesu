//go:build llmfake

package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/model"
)

func TestGenerateTests(t *testing.T) {
	c := client(t)
	saved := c.cfg.Models.ScenarioGenerator
	c.cfg.Models.ScenarioGenerator = "fake/generate"
	defer func() { c.cfg.Models.ScenarioGenerator = saved }()
	d, err := c.GenerateTests(context.Background(), model.GenerateRequest{Source: "# Mochi Shoes\nReturns within 30 days.", SourceName: "PRD.md", Target: "api", Type: "rest", Count: 2})
	require.NoError(t, err)
	require.Len(t, d.Tests, 2)
	assert.Equal(t, "refund inside the window", d.Tests[0].Name)
	assert.Equal(t, []string{"The agent accepts the return and explains the 30-day window"}, d.Tests[0].Judges)
	assert.NotEmpty(t, d.TraceID)

	d, err = c.GenerateTests(context.Background(), model.GenerateRequest{Source: "A shop.", Target: "web", Type: "browser"})
	require.NoError(t, err)
	require.NotEmpty(t, d.Tests)
	assert.NotEmpty(t, d.Tests[0].Steps, "a browser draft has steps")
	_, err = c.GenerateTests(context.Background(), model.GenerateRequest{Source: "  ", Target: "api"})
	assert.ErrorContains(t, err, "the document is empty")
}
