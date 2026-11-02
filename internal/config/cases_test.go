package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandCasesInline(t *testing.T) {
	s := &Suite{Suite: "s", Tests: []Test{
		{Name: "returns", Target: "api", Tags: []string{"smoke"}, Vars: map[string]string{"lang": "en", "days": "40"},
			Cases: []Case{{Name: "late", Vars: map[string]string{"days": "40"}}, {Vars: map[string]string{"days": "10", "lang": "es"}}}},
		{Name: "plain", Target: "api"},
	}}
	errs := ExpandCases(s, "suite.yaml")
	require.Empty(t, errs)
	require.Len(t, s.Tests, 3)
	assert.Equal(t, "returns [late]", s.Tests[0].Name)
	assert.Equal(t, []string{"smoke", "case:late"}, s.Tests[0].Tags)
	assert.Equal(t, map[string]string{"lang": "en", "days": "40"}, s.Tests[0].Vars)
	assert.Nil(t, s.Tests[0].Cases)
	assert.Equal(t, "returns [case 2]", s.Tests[1].Name, "an unnamed case is numbered")
	assert.Equal(t, map[string]string{"lang": "es", "days": "10"}, s.Tests[1].Vars, "case vars win over test vars")
	assert.Equal(t, "plain", s.Tests[2].Name)
}

func TestExpandCasesFromFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cases.csv"), []byte("name,question,expect\nlate,40 days ago,no\nearly,\"10 days, ago\",yes\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cases.json"), []byte(`[{"name":"one","n":1,"flag":true},{"n":2.5}]`), 0o644))
	s := &Suite{Suite: "s", Tests: []Test{
		{Name: "csv", Target: "api", CasesFile: "cases.csv"},
		{Name: "json", Target: "api", CasesFile: "cases.json"},
	}}
	errs := ExpandCases(s, filepath.Join(dir, "suite.yaml"))
	require.Empty(t, errs)
	require.Len(t, s.Tests, 4)
	assert.Equal(t, "csv [late]", s.Tests[0].Name)
	assert.Equal(t, map[string]string{"question": "40 days ago", "expect": "no"}, s.Tests[0].Vars)
	assert.Equal(t, map[string]string{"question": "10 days, ago", "expect": "yes"}, s.Tests[1].Vars)
	assert.Equal(t, "json [one]", s.Tests[2].Name)
	assert.Equal(t, map[string]string{"n": "1", "flag": "true"}, s.Tests[2].Vars, "scalars become strings")
	assert.Equal(t, "json [case 2]", s.Tests[3].Name)
	assert.Equal(t, map[string]string{"n": "2.5"}, s.Tests[3].Vars)
}

func TestExpandCasesErrors(t *testing.T) {
	s := &Suite{Suite: "s", Tests: []Test{
		{Name: "dup", Target: "api", Cases: []Case{{Name: "a", Vars: map[string]string{"x": "1"}}, {Name: "a", Vars: map[string]string{"x": "2"}}}},
		{Name: "missing", Target: "api", CasesFile: "nope.csv"},
	}}
	errs := ExpandCases(s, "suite.yaml")
	require.Len(t, errs, 2)
	assert.Contains(t, errs[0].Error(), `case "a" appears twice`)
	assert.Contains(t, errs[1].Error(), "cases_file nope.csv")
	assert.Len(t, s.Tests, 2, "nothing expanded on error")
	_, err := LoadCases("cases.txt")
	assert.Error(t, err)
}
