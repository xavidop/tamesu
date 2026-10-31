package llm

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing/fstest"
)

// promptDir is the directory of the .prompt files inside the prompt FS.
const promptDir = "prompts"

// DefaultPromptOverrideDir is where a project replaces built-in prompts.
const DefaultPromptOverrideDir = ".tamesu/prompts"

//go:embed prompts/*.prompt
var embeddedPrompts embed.FS

var versionRe = regexp.MustCompile(`(?m)^version:\s*"?([^"\n]+)"?\s*$`)

// promptSet is the merged prompt filesystem (embedded files, overrides on
// top) and the version each file declares in its front matter.
type promptSet struct {
	fs       fs.FS
	versions map[string]string
	// overridden lists the prompts a project replaced.
	overridden []string
}

// loadPrompts merges the embedded prompts with the files of overrideDir
// (default .tamesu/prompts) by name.
func loadPrompts(overrideDir string) (*promptSet, error) {
	if overrideDir == "" {
		overrideDir = DefaultPromptOverrideDir
	}
	merged := fstest.MapFS{}
	ps := &promptSet{fs: merged, versions: map[string]string{}}
	entries, err := fs.ReadDir(embeddedPrompts, promptDir)
	if err != nil {
		return nil, fmt.Errorf("llm: embedded prompts: %w", err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(embeddedPrompts, promptDir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		merged[promptDir+"/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	if overrides, err := os.ReadDir(overrideDir); err == nil {
		for _, e := range overrides {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".prompt") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(overrideDir, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("llm: prompt override %s: %w", e.Name(), err)
			}
			merged[promptDir+"/"+e.Name()] = &fstest.MapFile{Data: b}
			ps.overridden = append(ps.overridden, strings.TrimSuffix(e.Name(), ".prompt"))
		}
	}
	for path, f := range merged {
		name := strings.TrimSuffix(strings.TrimPrefix(path, promptDir+"/"), ".prompt")
		ps.versions[name] = versionOf(string(f.Data))
	}
	return ps, nil
}

// versionOf reads `version:` from a dotprompt front matter; "0" when absent.
func versionOf(src string) string {
	_, rest, ok := strings.Cut(src, "---\n")
	if !ok {
		return "0"
	}
	front, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return "0"
	}
	if m := versionRe.FindStringSubmatch(front); m != nil {
		return strings.TrimSpace(m[1])
	}
	return "0"
}

// Version returns the version of a prompt, as recorded in result.json.
func (p *promptSet) Version(name string) string {
	if v, ok := p.versions[name]; ok {
		return v
	}
	return "0"
}

// PromptVersions lists every prompt and its version.
func (c *Client) PromptVersions() map[string]string {
	out := make(map[string]string, len(c.prompts.versions))
	for k, v := range c.prompts.versions {
		out[k] = v
	}
	return out
}

// OverriddenPrompts lists the prompts the project replaced from .tamesu/prompts.
func (c *Client) OverriddenPrompts() []string { return append([]string(nil), c.prompts.overridden...) }
