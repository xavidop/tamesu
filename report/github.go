package report

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// GitHub speaks to GitHub Actions: a workflow annotation per failed or
// errored test (file and line of the test in its suite), the Markdown
// summary appended to the job summary when GITHUB_STEP_SUMMARY is set, and
// summary.md in the run directory.
type GitHub struct {
	dir string
	out io.Writer
	// stepSummary is the path of the job summary file; empty reads the
	// GITHUB_STEP_SUMMARY environment variable at Finish.
	stepSummary string
}

// NewGitHub returns the reporter; annotations go to out (standard output in
// a job).
func NewGitHub(dir string, out io.Writer) *GitHub { return &GitHub{dir: dir, out: out} }

func (*GitHub) RunStarted(RunInfo)                    {}
func (*GitHub) TestStarted(*TestOutcome)              {}
func (*GitHub) RunFinished(*TestOutcome, *RunOutcome) {}
func (*GitHub) TestFinished(*TestOutcome)             {}

// Finish writes the annotations, the job summary and summary.md.
func (g *GitHub) Finish(_ context.Context, s *Summary) error {
	for _, line := range Annotations(s) {
		_, _ = fmt.Fprintln(g.out, line)
	}
	md := RenderMarkdown(s)
	if err := os.WriteFile(filepath.Join(g.dir, MarkdownFile), []byte(md), 0o644); err != nil {
		return err
	}
	path := g.stepSummary
	if path == "" {
		path = os.Getenv("GITHUB_STEP_SUMMARY")
	}
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("job summary: %w", err)
		}
		_, _ = f.WriteString(md + "\n")
		return f.Close()
	}
	return nil
}

// Annotations are the workflow commands for the failed and errored tests:
// ::error file=<suite>,line=<n>,title=<test>::<reason>; a quarantined
// failure is a ::warning.
func Annotations(s *Summary) []string {
	var out []string
	for _, d := range failureDetails(s) {
		var t *TestOutcome
		for _, c := range s.Tests {
			if c.Suite+" / "+c.Name == d.test {
				t = c
				break
			}
		}
		if t == nil {
			continue
		}
		props := []string{}
		if t.File != "" {
			props = append(props, "file="+escapeProp(t.File))
			if t.Line > 0 {
				props = append(props, fmt.Sprintf("line=%d", t.Line))
			}
		}
		props = append(props, "title="+escapeProp("tamesu: "+t.Name))
		level := "::error "
		if d.quarantined {
			level = "::warning "
		}
		msg := d.reason
		if d.quarantined {
			msg = strings.TrimPrefix(d.quarantine, " ") + " " + msg
		}
		out = append(out, level+strings.Join(props, ",")+"::"+escapeData(msg))
	}
	return out
}

// escapeData and escapeProp follow GitHub's workflow command escaping.
func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
