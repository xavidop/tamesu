package browser

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// EnvChromium names the Chromium binary to use.
const EnvChromium = "TAMESU_CHROMIUM"

// cftVersions is Chrome for Testing's index of downloadable builds.
const cftVersions = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"

// FindChromium returns the Chromium binary to run: explicit, then
// TAMESU_CHROMIUM, then the usual install locations and PATH names, then (when
// download is true) Chrome for Testing downloaded into ~/.cache/tamesu.
func FindChromium(ctx context.Context, explicit string, download bool) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("chromium binary %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if p := os.Getenv(EnvChromium); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%s: %w", EnvChromium, p, err)
		}
		return p, nil
	}
	if p := lookInstalled(); p != "" {
		return p, nil
	}
	if cached := cachedChromium(); cached != "" {
		return cached, nil
	}
	if !download {
		return "", errors.New("no Chromium found: install Chrome or Chromium, or set TAMESU_CHROMIUM")
	}
	p, err := downloadChromium(ctx)
	if err != nil {
		return "", fmt.Errorf("no Chromium found and the download failed: %w (install Chrome or Chromium, or set TAMESU_CHROMIUM)", err)
	}
	return p, nil
}

// lookInstalled checks the common locations and PATH.
func lookInstalled() string {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
		}
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			candidates = append(candidates, filepath.Join(base, `Google\Chrome\Application\chrome.exe`), filepath.Join(base, `Chromium\Application\chrome.exe`), filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
		}
	default:
		candidates = []string{"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/snap/bin/chromium", "/usr/bin/microsoft-edge", "/usr/lib/chromium/chromium"}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "headless_shell", "msedge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// cacheDir is ~/.cache/tamesu/chromium.
func cacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "tamesu", "chromium")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "tamesu", "chromium")
	}
	return filepath.Join(home, ".cache", "tamesu", "chromium")
}

// platform and binary path inside a Chrome for Testing archive.
func cftPlatform() (platform, binary string) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "mac-arm64", "chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"
	case "darwin/amd64":
		return "mac-x64", "chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"
	case "linux/amd64":
		return "linux64", "chrome-linux64/chrome"
	case "windows/amd64":
		return "win64", "chrome-win64/chrome.exe"
	}
	return "", ""
}

func cachedChromium() string {
	_, binary := cftPlatform()
	if binary == "" {
		return ""
	}
	entries, err := os.ReadDir(cacheDir())
	if err != nil {
		return ""
	}
	// Newest version directory that has the binary.
	for i := len(entries) - 1; i >= 0; i-- {
		if !entries[i].IsDir() {
			continue
		}
		p := filepath.Join(cacheDir(), entries[i].Name(), binary)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// downloadChromium fetches the stable Chrome for Testing for this platform.
func downloadChromium(ctx context.Context) (string, error) {
	platform, binary := cftPlatform()
	if platform == "" {
		return "", fmt.Errorf("no Chrome for Testing build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cftVersions, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", cftVersions, res.Status)
	}
	var index struct {
		Channels map[string]struct {
			Version   string `json:"version"`
			Downloads struct {
				Chrome []struct {
					Platform string `json:"platform"`
					URL      string `json:"url"`
				} `json:"chrome"`
			} `json:"downloads"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(res.Body).Decode(&index); err != nil {
		return "", err
	}
	stable := index.Channels["Stable"]
	var dl string
	for _, d := range stable.Downloads.Chrome {
		if d.Platform == platform {
			dl = d.URL
		}
	}
	if dl == "" || stable.Version == "" {
		return "", fmt.Errorf("no stable Chrome for Testing download for %s", platform)
	}
	dir := filepath.Join(cacheDir(), stable.Version)
	if _, err := os.Stat(filepath.Join(dir, binary)); err == nil {
		return filepath.Join(dir, binary), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(dir, "chrome.zip")
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, dl, nil)
	if err != nil {
		return "", err
	}
	res2, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res2.Body.Close() }()
	if res2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", dl, res2.Status)
	}
	f, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, res2.Body); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := unzip(zipPath, dir); err != nil {
		return "", err
	}
	_ = os.Remove(zipPath)
	p := filepath.Join(dir, binary)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("the archive has no %s", binary)
	}
	return p, nil
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		target := filepath.Join(dst, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %q escapes the destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode()|0o600)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		_ = rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func epoch(secs float64) time.Time { return time.Unix(int64(secs), 0) }
