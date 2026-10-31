package tamesu

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSuite(t *testing.T) {
	s, err := LoadSuite("internal/config/testdata/valid/support-agent.yaml")
	require.NoError(t, err)
	assert.Equal(t, "support-agent", s.Name)
	require.Len(t, s.Tests, 4)
	assert.Equal(t, TestInfo{Name: "refund policy question", Target: "api", Type: "rest", Tags: []string{"smoke"}}, s.Tests[1])

	_, err = LoadSuite("internal/config/testdata/invalid/missing-target.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-target.yaml:8:")

	_, err = LoadSuite("internal/config/testdata/nope.yaml")
	require.ErrorContains(t, err, "tamesu: load")
}

func TestUUIDAndEnv(t *testing.T) {
	assert.Len(t, UUID(), 36)
	t.Setenv("TAMESU_TEST_ENV", "x")
	assert.Equal(t, "x", Env("TAMESU_TEST_ENV"))
	assert.Equal(t, "", Env("TAMESU_TEST_UNSET_ENV"))
}
