package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandPaths(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a/b.yaml", "a/c/d.yml", "e.yaml", "a/notes.txt"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755))
		require.NoError(t, writeFile(filepath.Join(dir, f), "suite: x\n"))
	}
	tests := []struct {
		name     string
		patterns []string
		want     []string
		wantErr  string
	}{
		{name: "glob", patterns: []string{"a/**/*.yaml"}, want: []string{"a/b.yaml"}},
		{name: "directory", patterns: []string{"a"}, want: []string{"a/b.yaml", "a/c/d.yml"}},
		{name: "file", patterns: []string{"e.yaml"}, want: []string{"e.yaml"}},
		{name: "dedup and sort", patterns: []string{"e.yaml", "**/*.yaml", "a"}, want: []string{"a/b.yaml", "a/c/d.yml", "e.yaml"}},
		{name: "no match", patterns: []string{"nope/**/*.yaml"}, wantErr: `no suites match "nope/**/*.yaml"`},
		{name: "missing file", patterns: []string{"zzz.yaml"}, wantErr: `no suites match "zzz.yaml"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandPaths(tt.patterns, dir)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
