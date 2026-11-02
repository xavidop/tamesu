package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
)

func TestGoldenFiles(t *testing.T) {
	dir := t.TempDir()
	suite := &config.Suite{Path: filepath.Join(dir, "suite.yaml")}
	transcript := []tassert.Turn{{Role: "user", Text: "hi"}, {Role: "agent", Text: "Hello Marta"}, {Role: "user", Text: "returns?"}, {Role: "agent", Text: "30 days"}}
	it := &item{suite: suite}
	r := &run{}

	// A missing file is written and the assertion passes with a note.
	a, err := r.buildGolden(it, &config.GoldenAssertion{File: "golden/refund.json"}, transcript)
	require.NoError(t, err)
	res := a.Evaluate(context.Background(), &tassert.RunResult{})
	assert.True(t, res.Pass)
	assert.Contains(t, res.Reason, "was missing, written from this run")
	ref, err := loadGolden(filepath.Join(dir, "golden", "refund.json"))
	require.NoError(t, err)
	assert.Equal(t, []string{"Hello Marta", "30 days"}, ref)

	// With the file present the reference is compared.
	a, err = r.buildGolden(it, &config.GoldenAssertion{File: "golden/refund.json", Mode: "exact"}, transcript)
	require.NoError(t, err)
	res = a.Evaluate(context.Background(), &tassert.RunResult{Transcript: transcript})
	assert.True(t, res.Pass, res.Reason)
	assert.Equal(t, "golden exact golden/refund.json", res.Name)
	changed := []tassert.Turn{{Role: "agent", Text: "Hello Marta"}, {Role: "agent", Text: "14 days"}}
	res = a.Evaluate(context.Background(), &tassert.RunResult{Transcript: changed})
	assert.False(t, res.Pass)

	// --update-golden rewrites it.
	r.o.UpdateGolden = true
	a, err = r.buildGolden(it, &config.GoldenAssertion{File: "golden/refund.json"}, changed)
	require.NoError(t, err)
	assert.True(t, a.Evaluate(context.Background(), &tassert.RunResult{}).Pass)
	ref, _ = loadGolden(filepath.Join(dir, "golden", "refund.json"))
	assert.Equal(t, []string{"Hello Marta", "14 days"}, ref)

	// A transcript.json works as a reference too; garbage does not.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "t.json"), []byte(`{"turns":[{"role":"user","text":"u"},{"role":"agent","text":"a1"}]}`), 0o644))
	ref, err = loadGolden(filepath.Join(dir, "t.json"))
	require.NoError(t, err)
	assert.Equal(t, []string{"a1"}, ref)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"x":1}`), 0o644))
	_, err = loadGolden(filepath.Join(dir, "bad.json"))
	assert.Error(t, err)
}
