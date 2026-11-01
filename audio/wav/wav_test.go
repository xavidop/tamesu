package wav

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoundTrip(t *testing.T) {
	left := []int16{0, 1000, -1000, 32767, -32768}
	right := []int16{5, 6, 7}
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, 48000, left, right))
	assert.Equal(t, 44+5*2*2, buf.Len())
	rate, ch, err := Read(&buf)
	require.NoError(t, err)
	assert.Equal(t, 48000, rate)
	require.Len(t, ch, 2)
	assert.Equal(t, left, ch[0])
	assert.Equal(t, []int16{5, 6, 7, 0, 0}, ch[1], "a shorter channel is padded")
}

func TestFileAndStreamingWriter(t *testing.T) {
	dir := t.TempDir()
	mono := filepath.Join(dir, "mono.wav")
	require.NoError(t, WriteFile(mono, 16000, []int16{1, 2, 3}))
	rate, ch, err := ReadFile(mono)
	require.NoError(t, err)
	assert.Equal(t, 16000, rate)
	assert.Equal(t, [][]int16{{1, 2, 3}}, ch)

	f, err := os.Create(filepath.Join(dir, "stream.wav"))
	require.NoError(t, err)
	w, err := NewWriter(f, 8000, 2)
	require.NoError(t, err)
	require.NoError(t, w.WriteFrames([]int16{1, 10, 2, 20}))
	require.NoError(t, w.WriteFrames([]int16{3, 30}))
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	rate, ch, err = ReadFile(f.Name())
	require.NoError(t, err)
	assert.Equal(t, 8000, rate)
	assert.Equal(t, [][]int16{{1, 2, 3}, {10, 20, 30}}, ch)
}

func TestReadRejectsOtherFormats(t *testing.T) {
	_, _, err := Read(bytes.NewReader([]byte("not a wav")))
	assert.Error(t, err)
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, 8000, []int16{1}))
	b := buf.Bytes()
	b[34] = 8 // bits per sample
	_, _, err = Read(bytes.NewReader(b))
	assert.ErrorContains(t, err, "8-bit")
	assert.Error(t, Write(&buf, 8000))
}
