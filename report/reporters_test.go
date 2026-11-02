package report

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
)

func reportersFixture() *Summary {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	return &Summary{StartedAt: t0, FinishedAt: t0.Add(3 * time.Second), Tests: []*TestOutcome{
		{Suite: "support", File: "tests/support.yaml", Line: 12, Name: "refund policy", Target: "api", Type: "rest", Passed: true, PassRate: 1, Repeat: 1, StartedAt: t0, FinishedAt: t0.Add(time.Second),
			Runs: []*RunOutcome{{Passed: true}}},
		{Suite: "support", File: "tests/support.yaml", Line: 30, Name: "no exceptions | strict", Target: "api", Type: "rest", Passed: false, Repeat: 1, StartedAt: t0, FinishedAt: t0.Add(2 * time.Second),
			Runs: []*RunOutcome{{Passed: false, Assertions: []tassert.Result{{Name: "judge \"no exception\"", Pass: false, Reason: "the agent promised a refund\nafter 40 days"}}}}},
		{Suite: "support", File: "tests/support.yaml", Line: 40, Name: "broken", Target: "api", Type: "rest", Passed: false, Repeat: 1,
			Runs: []*RunOutcome{{Error: "connection refused", ErrorKind: KindInfra}}},
	}}
}

func TestMarkdownAndGitHub(t *testing.T) {
	s := reportersFixture()
	md := RenderMarkdown(s)
	assert.Contains(t, md, "## ❌ tamesu: 1 passed, 1 failed, 1 errors")
	assert.Contains(t, md, "| ❌ | no exceptions \\| strict | support · api (rest) | 1 |")
	assert.Contains(t, md, "| 💥 | broken |")
	assert.Contains(t, md, "**support / no exceptions \\| strict**: judge \"no exception\": the agent promised a refund after 40 days")

	ann := Annotations(s)
	require.Len(t, ann, 2)
	assert.Equal(t, `::error file=tests/support.yaml,line=30,title=tamesu%3A no exceptions | strict::judge "no exception": the agent promised a refund%0Aafter 40 days`, ann[0])
	assert.Contains(t, ann[1], "line=40")
	assert.Contains(t, ann[1], "infra: connection refused")

	dir := t.TempDir()
	var out bytes.Buffer
	g := NewGitHub(dir, &out)
	g.stepSummary = filepath.Join(dir, "step.md")
	require.NoError(t, g.Finish(context.Background(), s))
	assert.Equal(t, 2, strings.Count(out.String(), "::error "))
	step, err := os.ReadFile(g.stepSummary)
	require.NoError(t, err)
	assert.Contains(t, string(step), "## ❌ tamesu")
	_, err = os.Stat(filepath.Join(dir, MarkdownFile))
	require.NoError(t, err)

	require.NoError(t, NewMarkdown(dir).Finish(context.Background(), s))
	b, _ := os.ReadFile(filepath.Join(dir, MarkdownFile))
	assert.Equal(t, md, string(b))
}

func TestSlack(t *testing.T) {
	var got map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := reportersFixture()
	sl := NewSlack(srv.URL, false)
	sl.Link = "https://ci.example.com/run/1"
	require.NoError(t, sl.Finish(context.Background(), s))
	text, _ := got["text"].(string)
	assert.True(t, strings.HasPrefix(text, ":x: *tamesu*: 1 passed, 1 failed, 1 errors in 3.0s"), text)
	assert.Contains(t, text, "• *support / broken*: infra: connection refused")
	assert.Contains(t, text, "https://ci.example.com/run/1")

	green := &Summary{Tests: []*TestOutcome{{Name: "ok", Passed: true, Runs: []*RunOutcome{{Passed: true}}}}}
	require.NoError(t, NewSlack(srv.URL, true).Finish(context.Background(), green))
	assert.Equal(t, 1, calls, "only_failures skips a green run")
	require.NoError(t, NewSlack(srv.URL, false).Finish(context.Background(), green))
	assert.Equal(t, 2, calls)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "invalid_token", http.StatusForbidden) }))
	defer bad.Close()
	err := NewSlack(bad.URL, false).Finish(context.Background(), s)
	assert.ErrorContains(t, err, "403")
}
