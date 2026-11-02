package fsutil

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache", "key.json")
	require.NoError(t, WriteFileAtomic(path, []byte("one"), 0o644))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "one", string(b))

	// Concurrent writers of one key leave exactly one complete file.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, WriteFileAtomic(path, []byte("twotwotwo"), 0o644))
		}()
	}
	wg.Wait()
	b, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "twotwotwo", string(b))
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary files are left behind")
}
