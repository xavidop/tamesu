package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/xavidop/tamesu/internal/config"
)

// These tests keep the user-facing documents true to the code. They are what
// .github/scripts/check_docs_claims.sh runs.

// repoFile reads a file relative to the repository root.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readmeSections are the "## " headings of the README, in order. The README
// is the front door: the pitch lives above the first heading and the detail
// in the docs site.
var readmeSections = []string{
	"Install", "Quick start", "Targets", "Agent skill", "Documentation", "Contributing", "License",
}

// globalFlags are the persistent flags every command accepts.
var globalFlags = map[string]bool{"--config": true, "--verbose": true, "--var": true, "--env-file": true, "--help": true}

// shippedTargets lists the target types that have a runner. A README targets
// row for any other type must say "planned". The targets modules extend this
// list as they land.
var shippedTargets = []string{"rest", "livekit", "sip", "browser", "realtime", "grpc", "pipecat", "model", "twilio", "mcp", "a2a", "vapi", "retell", "voiceflow"}

// mdSection returns the body of the "## <name>" section of text.
func mdSection(t *testing.T, file, text, name string) string {
	t.Helper()
	_, after, ok := strings.Cut(text, "\n## "+name+"\n")
	if !ok {
		t.Fatalf("%s has no \"## %s\" section", file, name)
	}
	if i := strings.Index(after, "\n## "); i >= 0 {
		after = after[:i]
	}
	return after
}

// outsideFences drops the lines inside ``` code fences.
func outsideFences(text string) []string {
	var out []string
	fence := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
			continue
		}
		if !fence {
			out = append(out, line)
		}
	}
	return out
}

// isBuiltin reports the commands the user docs do not list: those cobra adds
// itself and hidden developer commands.
func isBuiltin(c *cobra.Command) bool {
	return c.Name() == "help" || c.Name() == "completion" || c.Hidden
}

func userCommands(root *cobra.Command) map[string]*cobra.Command {
	cmds := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		if !isBuiltin(c) {
			cmds[c.Name()] = c
		}
	}
	return cmds
}

var flagRef = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// TestReadmeShape keeps the README in its fixed shape: the logo, "# 試す tamesu",
// the tagline, an italic one-liner, the three badges in order, then exactly
// the sections of readmeSections in that order, with the demo GIF before the
// first section.
func TestReadmeShape(t *testing.T) {
	readme := repoFile(t, "README.md")
	lines := outsideFences(readme)

	header := []struct {
		what string
		re   *regexp.Regexp
	}{
		{"logo", regexp.MustCompile(`^<img src="site/public/favicon\.svg"`)},
		{"title", regexp.MustCompile(`^# 試す (&nbsp;)?tamesu$`)},
		{"tagline", regexp.MustCompile(`^### Your agents, tested like users would\.$`)},
		{"italic one-liner", regexp.MustCompile(`^\*[^*].*$`)},
		{"Go Reference badge", regexp.MustCompile(`^\[!\[Go Reference\]`)},
		{"CI badge", regexp.MustCompile(`^\[!\[CI\]`)},
		{"MIT badge", regexp.MustCompile(`^\[!\[License: MIT\]`)},
	}
	next := 0
	firstSection := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") {
			firstSection = i
			break
		}
		if next < len(header) && header[next].re.MatchString(line) {
			next++
		}
	}
	if next < len(header) {
		t.Errorf("README.md header: missing or out of order from %q", header[next].what)
	}
	if firstSection < 0 {
		t.Fatal("README.md has no \"## \" section")
	}
	for _, f := range []string{"site/public/favicon.svg", "site/public/demo.gif"} {
		if _, err := os.Stat(filepath.Join("..", "..", f)); err != nil {
			t.Errorf("the README needs %s: %v", f, err)
		}
	}
	var got []string
	for _, line := range lines {
		if name, ok := strings.CutPrefix(line, "## "); ok {
			got = append(got, name)
		}
	}
	if !slices.Equal(got, readmeSections) {
		t.Errorf("README.md sections:\n got  %q\n want %q", got, readmeSections)
	}
	intro, _, _ := strings.Cut(readme, "\n## ")
	if !strings.Contains(intro, `src="site/public/demo.gif"`) {
		t.Error("README.md does not show site/public/demo.gif before its first section")
	}
}

// TestReadmeInstallMatchesTheRelease keeps the Install section true to
// .goreleaser.yaml: brew (macOS only, it is a cask), Scoop, the package
// formats, every OS the archives build, the Docker image, and go install of
// the CLI module.
func TestReadmeInstallMatchesTheRelease(t *testing.T) {
	install := mdSection(t, "README.md", repoFile(t, "README.md"), "Install")
	release := repoFile(t, ".goreleaser.yaml")
	for _, want := range []string{"brew install xavidop/tap/tamesu", "scoop install tamesu", "releases", "go install github.com/xavidop/tamesu/cmd/tamesu@latest"} {
		if !strings.Contains(install, want) {
			t.Errorf("README.md Install does not mention %q", want)
		}
	}
	if strings.Contains(release, "homebrew_casks:") && !strings.Contains(install, "macOS only") {
		t.Error("README.md Install: the release publishes a Homebrew cask, so brew must be called macOS only")
	}
	if strings.Contains(release, "scoops:") != strings.Contains(install, "Scoop") {
		t.Error("README.md Install and .goreleaser.yaml disagree about Scoop")
	}
	if image := regexp.MustCompile(`"(ghcr\.io/[a-z0-9/-]+):`).FindStringSubmatch(release); image != nil && !strings.Contains(install, image[1]) {
		t.Errorf("README.md Install does not mention the Docker image %s", image[1])
	}
	formats := regexp.MustCompile(`(?s)nfpms:.*?formats: \[([^\]]*)\]`).FindStringSubmatch(release)
	if strings.Contains(release, "nfpms:") && formats == nil {
		t.Fatal(".goreleaser.yaml has nfpms but no formats list")
	}
	if formats != nil {
		for _, f := range strings.Split(formats[1], ",") {
			if f = strings.TrimSpace(f); !strings.Contains(install, "."+f) {
				t.Errorf("README.md Install does not mention the .%s package the release builds", f)
			}
		}
	}
	names := map[string]string{"linux": "Linux", "darwin": "macOS", "windows": "Windows"}
	for _, m := range regexp.MustCompile(`(?m)^\s*goos: \[([^\]]*)\]`).FindAllStringSubmatch(release, -1) {
		for _, os := range strings.Split(m[1], ",") {
			if os = strings.TrimSpace(os); !strings.Contains(install, names[os]) {
				t.Errorf("README.md Install does not name %s, which the release builds", names[os])
			}
		}
	}
}

// TestReadmeCLIBlockMatchesTheCommands checks the README's command block (the
// ```text block of Quick start) against the cobra tree: every line names a
// real command, every command has a line, and every flag shown exists on the
// command or is global.
func TestReadmeCLIBlockMatchesTheCommands(t *testing.T) {
	section := mdSection(t, "README.md", repoFile(t, "README.md"), "Quick start")
	_, block, ok := strings.Cut(section, "```text\n")
	if !ok {
		t.Fatal("README.md Quick start has no ```text block of commands")
	}
	block, _, _ = strings.Cut(block, "```")
	cmds := userCommands(newRoot())
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
		command := regexp.MustCompile(`\s{2,}`).Split(line, 2)[0]
		fields := strings.Fields(command)
		if len(fields) < 2 || fields[0] != "tamesu" {
			t.Errorf("README.md command line %q does not start with \"tamesu <command>\"", line)
			continue
		}
		c := cmds[fields[1]]
		if c == nil {
			t.Errorf("README.md lists \"tamesu %s\", which does not exist", fields[1])
			continue
		}
		seen[fields[1]] = true
		have := map[string]bool{}
		for f := range globalFlags {
			have[f] = true
		}
		var add func(*cobra.Command)
		add = func(c *cobra.Command) {
			c.Flags().VisitAll(func(f *pflag.Flag) { have["--"+f.Name] = true })
			for _, s := range c.Commands() {
				add(s)
			}
		}
		add(c)
		for _, f := range flagRef.FindAllString(line, -1) {
			if !have[f] {
				t.Errorf("README.md line for \"tamesu %s\" shows %s, which it does not have", fields[1], f)
			}
		}
	}
	for name := range cmds {
		if !seen[name] {
			t.Errorf("README.md command block has no line for \"tamesu %s\"", name)
		}
	}
}

// TestReadmeTargetsTableMatchesConfig keeps the Targets table to exactly the
// target types of the suite schema, and makes a row for a type without a
// runner say so.
func TestReadmeTargetsTableMatchesConfig(t *testing.T) {
	section := mdSection(t, "README.md", repoFile(t, "README.md"), "Targets")
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9]+)` \\|(.*)$")
	var listed []string
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		listed = append(listed, m[1])
		if !slices.Contains(shippedTargets, m[1]) && !strings.Contains(strings.ToLower(m[2]), "planned") {
			t.Errorf("README.md Targets row for %q must say \"planned\": it has no runner yet", m[1])
		}
	}
	want := config.TargetTypes()
	if !slices.Equal(listed, want) {
		t.Errorf("README.md Targets rows %q; the schema's target types are %q", listed, want)
	}
}

// githubSlug is GitHub's anchor for a Markdown heading.
func githubSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestReadmeLinksResolve checks every link of the README that this repository
// can answer: a relative link names a file or directory that exists (and,
// into a Markdown file, a heading that exists), and a link into the docs site
// names a page the site has.
func TestReadmeLinksResolve(t *testing.T) {
	readme := repoFile(t, "README.md")
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	site := regexp.MustCompile(`^https://xavidop\.github\.io/tamesu/docs/([a-z0-9/-]*?)/?(#.*)?$`)
	for _, m := range link.FindAllStringSubmatch(readme, -1) {
		target := m[1]
		if sm := site.FindStringSubmatch(target); sm != nil {
			page := filepath.Join("..", "..", "site", "src", "pages", "docs", sm[1])
			if sm[1] == "" {
				page = filepath.Join(page, "index")
			}
			_, errFile := os.Stat(page + ".md")
			_, errIndex := os.Stat(filepath.Join(page, "index.md"))
			if errFile != nil && errIndex != nil {
				t.Errorf("README.md links to the docs page %q, which the site does not have", target)
			}
			continue
		}
		if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		path, frag, _ := strings.Cut(target, "#")
		if _, err := os.Stat(filepath.Join("..", "..", path)); err != nil {
			t.Errorf("README.md links to %q, which does not exist", target)
			continue
		}
		if frag != "" && strings.HasSuffix(path, ".md") {
			found := false
			for _, line := range outsideFences(repoFile(t, path)) {
				if h, ok := strings.CutPrefix(strings.TrimLeft(line, "#"), " "); ok && strings.HasPrefix(line, "#") && githubSlug(h) == frag {
					found = true
				}
			}
			if !found {
				t.Errorf("README.md links to %q, but %s has no such heading", target, path)
			}
		}
	}
}

// TestContributingLayoutMatchesTheModules keeps the "Repository layout" of
// CONTRIBUTING.md true to go.work: every module is named.
func TestContributingLayoutMatchesTheModules(t *testing.T) {
	const file = "CONTRIBUTING.md"
	layout := mdSection(t, file, repoFile(t, file), "Repository layout")
	work := repoFile(t, "go.work")
	for _, m := range regexp.MustCompile(`(?m)^\s*\./(\S+)$`).FindAllStringSubmatch(work, -1) {
		if !strings.Contains(layout, "`"+m[1]+"`") {
			t.Errorf("%s Repository layout does not name the module `%s`", file, m[1])
		}
	}
}

// TestSkillNamesEveryCommand keeps the agent skill's frontmatter description
// and body naming every command the CLI has.
func TestSkillNamesEveryCommand(t *testing.T) {
	skill := repoFile(t, "skills/tamesu/SKILL.md")
	_, rest, ok := strings.Cut(skill, "---\n")
	if !ok {
		t.Fatal("SKILL.md has no frontmatter")
	}
	frontmatter, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		t.Fatal("SKILL.md frontmatter is not closed")
	}
	if !strings.HasPrefix(frontmatter, "name: tamesu\n") {
		t.Error("SKILL.md frontmatter must start with name: tamesu")
	}
	for name := range userCommands(newRoot()) {
		if !strings.Contains(frontmatter, "`tamesu "+name+"`") {
			t.Errorf("SKILL.md description does not name `tamesu %s`", name)
		}
		if !strings.Contains(body, "tamesu "+name) {
			t.Errorf("SKILL.md body does not mention tamesu %s", name)
		}
	}
	for _, kind := range config.AssertionKinds {
		if !strings.Contains(frontmatter, kind) {
			t.Errorf("SKILL.md description does not name the %s assertion", kind)
		}
	}
	for _, typ := range config.TargetTypes() {
		if !strings.Contains(frontmatter, typ) {
			t.Errorf("SKILL.md description does not name the %s target", typ)
		}
	}
}
