package pinned

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// tarball packs name → content, every file executable.
func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// release serves one file as a GitHub release would and counts the requests.
func release(t *testing.T, tool Tool, asset Asset, body []byte) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/"+tool.Repo+"/releases/download/v"+tool.Version+"/"+asset.Name {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

func TestPinsCoverBothTools(t *testing.T) {
	for _, name := range []string{AgentBrowser, AgentDesktop} {
		tool, err := Lookup(name)
		require.NoError(t, err)
		assert.Regexp(t, `^\d+\.\d+\.\d+$`, tool.Version)
		assert.NotEmpty(t, tool.Repo)
		for _, p := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64"} {
			a, ok := tool.Assets[p]
			require.True(t, ok, "%s has no %s build", name, p)
			assert.Len(t, a.SHA256, 64)
			assert.NotEmpty(t, a.Name)
		}
	}
	_, err := Lookup("nope")
	assert.ErrorContains(t, err, `no pin for "nope"`)

	tool, _ := Lookup(AgentBrowser)
	assert.Equal(t, "TAMESU_AGENT_BROWSER", tool.EnvVar())
}

func TestFindDownloadsVerifiesAndCaches(t *testing.T) {
	t.Setenv("TAMESU_EXAMPLE_TOOL", "")
	body := []byte("#!/bin/sh\necho example-tool 1.2.3\n")
	asset := Asset{Name: "example-tool-bin", SHA256: sha(body)}
	tool := Tool{Name: "example-tool", Repo: "acme/example-tool", Version: "1.2.3", Assets: map[string]Asset{Platform(): asset}}
	base, hits := release(t, tool, asset, body)
	o := Options{CacheDir: t.TempDir(), BaseURL: base}

	_, ok := tool.Cached(o)
	assert.False(t, ok)

	found, err := tool.Find(context.Background(), o)
	require.NoError(t, err)
	assert.True(t, found.Pinned)
	assert.Equal(t, filepath.Join(o.CacheDir, "example-tool", "1.2.3", tool.binary()), found.Path)
	got, err := os.ReadFile(found.Path)
	require.NoError(t, err)
	assert.Equal(t, body, got)
	if runtime.GOOS != "windows" {
		st, err := os.Stat(found.Path)
		require.NoError(t, err)
		assert.NotZero(t, st.Mode()&0o100, "the binary is executable")
		v, err := VersionOf(context.Background(), found.Path)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3", v)
	}

	// The second lookup is served by the cache.
	again, err := tool.Find(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, found.Path, again.Path)
	assert.EqualValues(t, 1, hits.Load())

	// Nothing but the version directory is left behind.
	entries, err := os.ReadDir(filepath.Join(o.CacheDir, "example-tool"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "1.2.3", entries[0].Name())
}

func TestFindRefusesAWrongChecksum(t *testing.T) {
	t.Setenv("TAMESU_EXAMPLE_TOOL", "")
	asset := Asset{Name: "example-tool-bin", SHA256: sha([]byte("what was pinned"))}
	tool := Tool{Name: "example-tool", Repo: "acme/example-tool", Version: "1.2.3", Assets: map[string]Asset{Platform(): asset}}
	base, _ := release(t, tool, asset, []byte("something else"))
	o := Options{CacheDir: t.TempDir(), BaseURL: base}

	_, err := tool.Find(context.Background(), o)
	require.Error(t, err)
	assert.ErrorContains(t, err, "refusing to use it")
	assert.ErrorContains(t, err, "TAMESU_EXAMPLE_TOOL")
	_, ok := tool.Cached(o)
	assert.False(t, ok, "a file that failed the check is not installed")
	entries, _ := os.ReadDir(filepath.Join(o.CacheDir, "example-tool"))
	assert.Empty(t, entries)
}

func TestFindUnpacksAnArchiveWithItsHelper(t *testing.T) {
	t.Setenv("TAMESU_EXAMPLE_TOOL", "")
	tool := Tool{Name: "example-tool", Repo: "acme/example-tool", Version: "2.0.0"}
	body := tarball(t, map[string]string{tool.binary(): "main", "example-tool-helper": "helper"})
	asset := Asset{Name: "example-tool-v2.0.0.tar.gz", SHA256: sha(body)}
	tool.Assets = map[string]Asset{Platform(): asset}
	base, _ := release(t, tool, asset, body)
	o := Options{CacheDir: t.TempDir(), BaseURL: base}

	found, err := tool.Find(context.Background(), o)
	require.NoError(t, err)
	helper, err := os.ReadFile(filepath.Join(filepath.Dir(found.Path), "example-tool-helper"))
	require.NoError(t, err)
	assert.Equal(t, "helper", string(helper))
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(filepath.Dir(found.Path), "example-tool-helper"))
		require.NoError(t, err)
		assert.NotZero(t, st.Mode()&0o100, "the helper stays executable")
	}
}

func TestFindRefusesAnArchiveThatEscapes(t *testing.T) {
	t.Setenv("TAMESU_EXAMPLE_TOOL", "")
	tool := Tool{Name: "example-tool", Repo: "acme/example-tool", Version: "2.0.0"}
	body := tarball(t, map[string]string{"../../evil": "x"})
	asset := Asset{Name: "example-tool-v2.0.0.tar.gz", SHA256: sha(body)}
	tool.Assets = map[string]Asset{Platform(): asset}
	base, _ := release(t, tool, asset, body)
	cache := t.TempDir()

	_, err := tool.Find(context.Background(), Options{CacheDir: cache, BaseURL: base})
	assert.ErrorContains(t, err, "escapes the destination")
	_, serr := os.Stat(filepath.Join(filepath.Dir(cache), "evil"))
	assert.True(t, os.IsNotExist(serr))
}

func TestFindPrefersTheVariableAndTheExplicitPath(t *testing.T) {
	tool := Tool{Name: "example-tool", Repo: "acme/example-tool", Version: "1.2.3", Assets: map[string]Asset{}}
	mine := filepath.Join(t.TempDir(), "mine")
	require.NoError(t, os.WriteFile(mine, []byte("x"), 0o755))

	t.Setenv("TAMESU_EXAMPLE_TOOL", mine)
	found, err := tool.Find(context.Background(), Options{CacheDir: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, Found{Path: mine}, found)

	t.Setenv("TAMESU_EXAMPLE_TOOL", filepath.Join(t.TempDir(), "missing"))
	_, err = tool.Find(context.Background(), Options{CacheDir: t.TempDir()})
	assert.ErrorContains(t, err, "TAMESU_EXAMPLE_TOOL=")

	found, err = tool.Find(context.Background(), Options{Explicit: mine})
	require.NoError(t, err)
	assert.False(t, found.Pinned)
}

func TestFindWithoutABuildOrADownload(t *testing.T) {
	t.Setenv("TAMESU_EXAMPLE_TOOL", "")
	tool := Tool{Name: "example-tool", Repo: "acme/example-tool", Version: "1.2.3", Assets: map[string]Asset{"plan9/mips": {Name: "x", SHA256: "y"}}}
	_, err := tool.Find(context.Background(), Options{CacheDir: t.TempDir()})
	assert.ErrorContains(t, err, "has no build for "+Platform())
	assert.ErrorContains(t, err, "plan9/mips")

	tool.Assets[Platform()] = Asset{Name: "x", SHA256: "y"}
	_, err = tool.Find(context.Background(), Options{CacheDir: t.TempDir(), NoDownload: true})
	assert.ErrorContains(t, err, "downloads are off")
}
