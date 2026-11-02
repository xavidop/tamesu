package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestThirdPartyNoticesCoverVendoredCode keeps THIRD_PARTY_NOTICES.md in
// step with what the tree vendors or loads.
func TestThirdPartyNoticesCoverVendoredCode(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_NOTICES.md"))
	require.NoError(t, err)
	notices := string(b)
	for _, want := range []string{"axe-core", "Silero", "Pipecat", "libopus", "ONNX Runtime", "Chrome for Testing", "THIRD_PARTY_LICENSES.csv"} {
		assert.Contains(t, notices, want, "THIRD_PARTY_NOTICES.md does not mention %s", want)
	}
	for _, file := range []string{"targets/browser/assets/AXE-LICENSE", "audio/vad/silero_vad.onnx", "targets/pipecat/frames/frames.proto", "THIRD_PARTY_LICENSES.csv"} {
		_, err := os.Stat(filepath.Join(root, file))
		assert.NoError(t, err, "%s named in the notices is missing", file)
	}
	csv, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_LICENSES.csv"))
	require.NoError(t, err)
	assert.Greater(t, strings.Count(string(csv), "\n"), 100, "the license list looks empty; run make notices")
	rel, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, strings.Count(string(rel), "- THIRD_PARTY_NOTICES.md\n      - THIRD_PARTY_LICENSES.csv"), 2, "both archives ship the notices")
}
