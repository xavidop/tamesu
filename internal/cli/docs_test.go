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

	"github.com/xavidop/tamesu/internal/cli"
)

const docsDir = "../../site/src/pages/docs"

func newRoot() *cobra.Command {
	return cli.NewRoot(cli.BuildInfo{}, cli.Streams{}, cli.Deps{})
}

// requireSite skips a docs test while the site has no docs pages yet.
func requireSite(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(docsDir, "cli.md")); err != nil {
		t.Skip("the docs site has no cli.md yet")
	}
}

// sitePage reads the docs section <name>: <name>.md, then each page under
// <name>/, in name order.
func sitePage(t *testing.T, name string) string {
	t.Helper()
	base := filepath.Join(docsDir, name)
	var doc strings.Builder
	if b, err := os.ReadFile(base + ".md"); err == nil {
		doc.Write(b)
	}
	pages, err := filepath.Glob(filepath.Join(base, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(pages)
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		doc.WriteString("\n")
		doc.Write(b)
	}
	if doc.Len() == 0 {
		t.Fatalf("the docs site has no %s.md and no %s/*.md", name, name)
	}
	return doc.String()
}

// docSections splits the CLI reference into the text before the first
// "## tamesu <name>" heading and one body per such heading.
func docSections(doc string) (intro string, sections map[string]string) {
	heading := regexp.MustCompile(`(?m)^## tamesu (\S+)\s*$`)
	idx := heading.FindAllStringSubmatchIndex(doc, -1)
	sections = map[string]string{}
	intro = doc
	if len(idx) > 0 {
		intro = doc[:idx[0][0]]
	}
	anyHeading := regexp.MustCompile(`(?m)^## `)
	for _, m := range idx {
		end := len(doc)
		if next := anyHeading.FindStringIndex(doc[m[1]:]); next != nil {
			end = m[1] + next[0]
		}
		sections[doc[m[2]:m[3]]] = doc[m[1]:end]
	}
	return intro, sections
}

func localFlags(c *cobra.Command) []string {
	var names []string
	c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name != "help" {
			names = append(names, "--"+f.Name)
		}
	})
	return names
}

// TestDocsCoverCommandsAndFlags fails when the CLI reference and the cobra
// command tree disagree: a command or flag the code has but the page lacks,
// or a "## tamesu <name>" heading or flags-table row the code does not have.
func TestDocsCoverCommandsAndFlags(t *testing.T) {
	requireSite(t)
	doc := sitePage(t, "cli")
	intro, sections := docSections(doc)
	root := newRoot()

	for f := range globalFlags {
		if f != "--help" && !strings.Contains(intro, f) {
			t.Errorf("cli.md intro does not mention the global flag %s", f)
		}
	}
	known := userCommands(root)
	for name, c := range known {
		body, ok := sections[name]
		if !ok {
			t.Errorf("cli.md has no \"## tamesu %s\" heading", name)
			continue
		}
		if !strings.Contains(body, "tamesu "+name) {
			t.Errorf("cli.md section %q does not mention \"tamesu %s\"", name, name)
		}
		for _, f := range localFlags(c) {
			if !strings.Contains(body, f) {
				t.Errorf("cli.md section %q does not mention %s", name, f)
			}
		}
	}
	for name := range sections {
		if _, ok := known[name]; !ok {
			t.Errorf("cli.md documents \"tamesu %s\", which does not exist", name)
		}
	}
	// Every flag in a flags-table row must exist on that command or be global.
	row := regexp.MustCompile("(?m)^\\| `(?:-\\w, )?(--[a-z-]+)")
	for name, body := range sections {
		c := known[name]
		if c == nil {
			continue
		}
		have := map[string]bool{}
		for f := range globalFlags {
			have[f] = true
		}
		c.Flags().VisitAll(func(f *pflag.Flag) { have["--"+f.Name] = true })
		for _, m := range row.FindAllStringSubmatch(body, -1) {
			if !have[m[1]] {
				t.Errorf("cli.md section %q lists %s, which \"tamesu %s\" does not have", name, m[1], name)
			}
		}
	}
	// The exit codes are documented.
	for _, code := range []string{"`0`", "`1`", "`2`", "`3`", "`4`"} {
		if !strings.Contains(doc, code) {
			t.Errorf("cli.md does not document exit code %s", code)
		}
	}
}

// TestSiteNavMatchesPages keeps the sidebar of DocsLayout.astro and the pages
// under site/src/pages/docs in step: every slug has a page and every page is
// in the sidebar.
func TestSiteNavMatchesPages(t *testing.T) {
	requireSite(t)
	layout, err := os.ReadFile("../../site/src/layouts/DocsLayout.astro")
	if err != nil {
		t.Fatal(err)
	}
	slugRe := regexp.MustCompile(`slug: "([a-z0-9/-]*)"`)
	slugs := map[string]bool{}
	for _, m := range slugRe.FindAllStringSubmatch(string(layout), -1) {
		slugs[m[1]] = true
		page := filepath.Join(docsDir, m[1])
		if m[1] == "" {
			page = filepath.Join(docsDir, "index")
		}
		_, errFile := os.Stat(page + ".md")
		_, errIndex := os.Stat(filepath.Join(page, "index.md"))
		if errFile != nil && errIndex != nil {
			t.Errorf("DocsLayout.astro links slug %q, which has no page", m[1])
		}
	}
	err = filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		rel, _ := filepath.Rel(docsDir, path)
		slug := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		slug = strings.TrimSuffix(slug, "/index")
		if slug == "index" {
			slug = ""
		}
		if !slugs[slug] {
			t.Errorf("docs page %s is not in the sidebar of DocsLayout.astro", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
