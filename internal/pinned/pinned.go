// Package pinned finds the external command-line tools tamesu drives
// (agent-browser, agent-desktop) at the version tamesu was tested with.
//
// pins.json names each tool's release and the SHA-256 of the file of every
// platform; .github/scripts/update_cli_pins.py rewrites it. A tool is taken
// from the variable that names a binary (TAMESU_AGENT_BROWSER,
// TAMESU_AGENT_DESKTOP), else from tamesu's cache, else it is downloaded into
// the cache and checked against the pin before anything runs it. A binary
// that happens to be on PATH is not used: a pin means everyone runs the same
// version.
package pinned

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// The tools.
const (
	AgentBrowser = "agent-browser"
	AgentDesktop = "agent-desktop"
)

//go:embed pins.json
var pinsJSON []byte

// Asset is one platform's release file.
type Asset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Tool is a pinned tool: its repository, the version and the file of each
// platform (keyed GOOS/GOARCH, with linux-musl for a musl libc).
type Tool struct {
	Name    string           `json:"-"`
	Repo    string           `json:"repo"`
	Version string           `json:"version"`
	Assets  map[string]Asset `json:"assets"`
}

// Options change where a tool is looked for; the zero value is what a run uses.
type Options struct {
	// Explicit is a binary to use as is; empty reads the tool's variable.
	Explicit string
	// NoDownload fails instead of fetching a tool that is not in the cache.
	NoDownload bool
	// CacheDir replaces ~/.cache/tamesu.
	CacheDir string
	// BaseURL replaces https://github.com as the host of the release files.
	BaseURL string
	// Client fetches the release file.
	Client *http.Client
}

// Found is a tool ready to run.
type Found struct {
	Path string
	// Pinned is false for a binary named by the variable or Options.Explicit,
	// whose version is whatever the user installed.
	Pinned bool
}

var (
	loadOnce sync.Once
	loaded   map[string]Tool
	loadErr  error
	// installing serialises the downloads of one process.
	installing sync.Mutex
)

func load() (map[string]Tool, error) {
	loadOnce.Do(func() {
		loadErr = json.Unmarshal(pinsJSON, &loaded)
		for name, t := range loaded {
			t.Name = name
			loaded[name] = t
		}
	})
	return loaded, loadErr
}

// Lookup returns the pin of a tool.
func Lookup(name string) (Tool, error) {
	tools, err := load()
	if err != nil {
		return Tool{}, fmt.Errorf("pinned: pins.json: %w", err)
	}
	t, ok := tools[name]
	if !ok {
		return Tool{}, fmt.Errorf("pinned: no pin for %q", name)
	}
	return t, nil
}

// EnvVar is the variable that names a binary to use instead of the pinned
// one: TAMESU_AGENT_BROWSER for agent-browser.
func (t Tool) EnvVar() string {
	return "TAMESU_" + strings.ToUpper(strings.ReplaceAll(t.Name, "-", "_"))
}

// Platform is this machine's key in a pin's assets.
func Platform() string {
	if runtime.GOOS == "linux" && musl() {
		return "linux-musl/" + runtime.GOARCH
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

// musl reports a musl libc (Alpine), which needs builds of its own.
func musl() bool {
	m, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	return len(m) > 0
}

// binary is the tool's file name on this machine.
func (t Tool) binary() string {
	if runtime.GOOS == "windows" {
		return t.Name + ".exe"
	}
	return t.Name
}

func (o Options) cacheDir() string {
	if o.CacheDir != "" {
		return o.CacheDir
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "tamesu")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "tamesu")
	}
	return filepath.Join(home, ".cache", "tamesu")
}

// dir is where the pinned version lives in the cache.
func (t Tool) dir(o Options) string {
	return filepath.Join(o.cacheDir(), t.Name, t.Version)
}

// url is where the release file is fetched from.
func (t Tool) url(o Options, a Asset) string {
	base := strings.TrimSuffix(o.BaseURL, "/")
	if base == "" {
		base = "https://github.com"
	}
	return fmt.Sprintf("%s/%s/releases/download/v%s/%s", base, t.Repo, t.Version, a.Name)
}

// Cached returns the pinned binary when the cache has it.
func (t Tool) Cached(o Options) (string, bool) {
	p := filepath.Join(t.dir(o), t.binary())
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	return "", false
}

// Find returns the tool: the binary Options.Explicit or the tool's variable
// names, else the pinned version from the cache, else a download of it.
func (t Tool) Find(ctx context.Context, o Options) (Found, error) {
	explicit, source := o.Explicit, "the configured path"
	if explicit == "" {
		explicit, source = os.Getenv(t.EnvVar()), t.EnvVar()
	}
	if explicit != "" {
		if st, err := os.Stat(explicit); err != nil || st.IsDir() {
			return Found{}, fmt.Errorf("%s: %s=%s is not a file", t.Name, source, explicit)
		}
		return Found{Path: explicit}, nil
	}
	if p, ok := t.Cached(o); ok {
		return Found{Path: p, Pinned: true}, nil
	}
	asset, ok := t.Assets[Platform()]
	if !ok {
		return Found{}, fmt.Errorf("%s %s has no build for %s (builds: %s); set %s to a binary of your own", t.Name, t.Version, Platform(), strings.Join(t.platforms(), ", "), t.EnvVar())
	}
	if o.NoDownload {
		return Found{}, fmt.Errorf("%s %s is not in %s and downloads are off; set %s to an installed binary", t.Name, t.Version, t.dir(o), t.EnvVar())
	}
	installing.Lock()
	defer installing.Unlock()
	if p, ok := t.Cached(o); ok {
		return Found{Path: p, Pinned: true}, nil
	}
	if err := t.install(ctx, o, asset); err != nil {
		return Found{}, fmt.Errorf("%s %s: %w (set %s to an installed binary to skip the download)", t.Name, t.Version, err, t.EnvVar())
	}
	p, ok := t.Cached(o)
	if !ok {
		return Found{}, fmt.Errorf("%s %s: %s has no %s", t.Name, t.Version, asset.Name, t.binary())
	}
	return Found{Path: p, Pinned: true}, nil
}

func (t Tool) platforms() []string {
	out := make([]string, 0, len(t.Assets))
	for k := range t.Assets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// install downloads the release file, checks it against the pin and only
// then unpacks it into the cache.
func (t Tool) install(ctx context.Context, o Options, a Asset) error {
	final := t.dir(o)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(final), "."+t.Version+"-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	u := t.url(o, a)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", u, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", u, res.Status)
	}
	raw := filepath.Join(work, "download")
	f, err := os.OpenFile(raw, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, sum), res.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", u, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("%s has SHA-256 %s, the pin says %s: refusing to use it", a.Name, got, a.SHA256)
	}

	out := filepath.Join(work, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(a.Name, ".tar.gz") {
		if err := untar(raw, out); err != nil {
			return fmt.Errorf("unpack %s: %w", a.Name, err)
		}
	} else if err := os.Rename(raw, filepath.Join(out, t.binary())); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(out, t.binary()), 0o755); err != nil {
		return fmt.Errorf("%s has no %s: %w", a.Name, t.binary(), err)
	}
	// Another process may have installed the same version meanwhile.
	if err := os.Rename(out, final); err != nil {
		if _, ok := t.Cached(o); ok {
			return nil
		}
		return err
	}
	return nil
}

// untar unpacks the regular files of a .tar.gz into dst, keeping their
// permissions (a tool's helper has to stay executable beside it).
func untar(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	root := filepath.Clean(dst) + string(os.PathSeparator)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(target, root) {
			return fmt.Errorf("entry %q escapes the destination", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// VersionOf asks a binary for its version (`--version`).
func VersionOf(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", path, err)
	}
	if v := versionRe.FindString(string(out)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s --version printed no version: %q", path, strings.TrimSpace(string(out)))
}
