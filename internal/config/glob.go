package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ExpandPaths turns suite paths and globs into a sorted, deduplicated list of
// files. A literal file is kept as is, a literal directory means every .yaml
// and .yml under it, and ** matches across directories. Relative patterns are
// resolved against cwd ("" for the working directory) and returned relative to
// it. A pattern that matches nothing is an error.
func ExpandPaths(patterns []string, cwd string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.ToSlash(filepath.Clean(p))
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if !strings.ContainsAny(pattern, "*?[{") {
			full := pattern
			if !filepath.IsAbs(full) && cwd != "" {
				full = filepath.Join(cwd, pattern)
			}
			info, err := os.Stat(full)
			if err != nil {
				return nil, fmt.Errorf("no suites match %q: %w", pattern, err)
			}
			if !info.IsDir() {
				add(pattern)
				continue
			}
			matches, err := globIn(cwd, []string{filepath.ToSlash(filepath.Join(pattern, "**", "*.yaml")), filepath.ToSlash(filepath.Join(pattern, "**", "*.yml"))})
			if err != nil {
				return nil, err
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("no suites match %q: the directory has no .yaml or .yml files", pattern)
			}
			for _, m := range matches {
				add(m)
			}
			continue
		}
		matches, err := globIn(cwd, []string{pattern})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no suites match %q", pattern)
		}
		for _, m := range matches {
			add(m)
		}
	}
	sort.Strings(out)
	return out, nil
}

func globIn(cwd string, patterns []string) ([]string, error) {
	var out []string
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(pattern)
		var matches []string
		var err error
		if filepath.IsAbs(pattern) {
			matches, err = doublestar.FilepathGlob(pattern, doublestar.WithFilesOnly())
		} else {
			root := cwd
			if root == "" {
				root = "."
			}
			matches, err = doublestar.Glob(os.DirFS(root), strings.TrimPrefix(pattern, "./"), doublestar.WithFilesOnly())
		}
		if err != nil {
			return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
		}
		out = append(out, matches...)
	}
	return out, nil
}
