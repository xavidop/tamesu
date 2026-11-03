package report

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Merge combines the summaries of several run directories into one run
// directory: the shards of one suite, or repeated runs of the same tests.
// Tests with the same suite and name are merged into one outcome whose runs
// are the runs of every source, in order; every run directory is copied
// under outDir/tests so the report's links keep working. Labels are the
// union of the sources (differing values are joined with a comma), usage and
// cache counts add up, and the merged summary.json, junit.xml, report.html
// and summary.md are written into outDir.
func Merge(dirs []string, outDir string) (*Summary, error) {
	if len(dirs) == 0 {
		return nil, errors.New("merge: no run directories")
	}
	out, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	merged := &Summary{OutDir: out, Labels: map[string]string{}}
	byKey := map[string]*TestOutcome{}
	slugs := map[string]string{} // key -> slug under out/tests
	usedSlug := map[string]bool{}
	labelValues := map[string]map[string]bool{}
	for _, dir := range dirs {
		src, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		if src == out {
			return nil, fmt.Errorf("merge: %s is the output directory", dir)
		}
		s, err := LoadSummary(src)
		if err != nil {
			return nil, fmt.Errorf("merge: %w", err)
		}
		if merged.StartedAt.IsZero() || (!s.StartedAt.IsZero() && s.StartedAt.Before(merged.StartedAt)) {
			merged.StartedAt = s.StartedAt
		}
		if s.FinishedAt.After(merged.FinishedAt) {
			merged.FinishedAt = s.FinishedAt
		}
		if merged.Version == "" {
			merged.Version = s.Version
		}
		if merged.StoppedEarly == "" {
			merged.StoppedEarly = s.StoppedEarly
		}
		merged.BudgetExceeded = merged.BudgetExceeded || s.BudgetExceeded
		merged.Usage.Calls += s.Usage.Calls
		merged.Usage.InputTokens += s.Usage.InputTokens
		merged.Usage.OutputTokens += s.Usage.OutputTokens
		merged.Usage.CostUSD += s.Usage.CostUSD
		if s.LLMCache != nil {
			if merged.LLMCache == nil {
				merged.LLMCache = &CacheStats{}
			}
			merged.LLMCache.Hits += s.LLMCache.Hits
			merged.LLMCache.Misses += s.LLMCache.Misses
		}
		for k, v := range s.Labels {
			if labelValues[k] == nil {
				labelValues[k] = map[string]bool{}
			}
			labelValues[k][v] = true
		}
		for _, t := range s.Tests {
			key := t.Suite + " / " + t.Name
			dst, ok := byKey[key]
			if !ok {
				slug := filepath.Base(t.Dir)
				if slug == "" || slug == "." || slug == string(filepath.Separator) {
					slug = slugOf(t.Name)
				}
				for i := 2; usedSlug[slug]; i++ {
					slug = fmt.Sprintf("%s-%d", filepath.Base(t.Dir), i)
				}
				usedSlug[slug] = true
				slugs[key] = slug
				c := *t
				c.Runs = nil
				c.Repeat = 0
				c.Dir = filepath.Join(out, "tests", slug)
				dst = &c
				byKey[key] = dst
				merged.Tests = append(merged.Tests, dst)
			}
			if dst.StartedAt.IsZero() || (!t.StartedAt.IsZero() && t.StartedAt.Before(dst.StartedAt)) {
				dst.StartedAt = t.StartedAt
			}
			if dst.Quarantine == nil {
				dst.Quarantine = t.Quarantine
			}
			if dst.Load == nil {
				dst.Load = t.Load
			}
			for _, r := range t.Runs {
				if r == nil {
					continue
				}
				c := *r
				c.Index = len(dst.Runs) + 1
				c.Dir = filepath.Join(dst.Dir, fmt.Sprintf("run-%d", c.Index))
				if r.Dir != "" {
					srcRun := r.Dir
					if !filepath.IsAbs(srcRun) {
						srcRun = filepath.Join(src, srcRun)
					}
					if info, err := os.Stat(srcRun); err == nil && info.IsDir() {
						if err := copyDir(srcRun, c.Dir); err != nil {
							return nil, fmt.Errorf("merge: copy %s: %w", srcRun, err)
						}
					}
				}
				dst.Runs = append(dst.Runs, &c)
			}
			dst.Repeat += t.Repeat
			if dst.Repeat < len(dst.Runs) {
				dst.Repeat = len(dst.Runs)
			}
		}
	}
	for _, t := range merged.Tests {
		finished := t.FinishedAt
		t.Finalize(finished)
		if finished.IsZero() {
			t.FinishedAt = merged.FinishedAt
		}
	}
	keys := make([]string, 0, len(labelValues))
	for k := range labelValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := make([]string, 0, len(labelValues[k]))
		for v := range labelValues[k] {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		merged.Labels[k] = strings.Join(vals, ",")
	}
	if len(merged.Labels) == 0 {
		merged.Labels = nil
	}
	if err := os.MkdirAll(filepath.Join(out, "tests"), 0o755); err != nil {
		return nil, err
	}
	ctx := context.Background()
	for _, rep := range []Reporter{NewJSON(out), NewJUnit(out), NewHTML(out), NewMarkdown(out)} {
		if err := rep.Finish(ctx, merged); err != nil {
			return nil, fmt.Errorf("merge: %w", err)
		}
	}
	return merged, nil
}

// copyDir copies a run directory (files and subdirectories) to dst.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return os.CopyFS(dst, os.DirFS(src))
}

// slugOf is a file-name-safe form of a test name, as the runner makes.
func slugOf(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
