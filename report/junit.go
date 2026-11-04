package report

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// JUnit writes junit.xml into the run directory: one testcase per test with
// its repeats aggregated, failure messages carrying the judge reasons, and
// the suite file and line of each test.
type JUnit struct{ dir string }

// NewJUnit returns the JUnit reporter for the run directory.
func NewJUnit(dir string) *JUnit { return &JUnit{dir: dir} }

func (*JUnit) RunStarted(RunInfo)                    {}
func (*JUnit) TestStarted(*TestOutcome)              {}
func (*JUnit) RunFinished(*TestOutcome, *RunOutcome) {}
func (*JUnit) TestFinished(*TestOutcome)             {}

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr,omitempty"`
	Time     string       `xml:"time,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr,omitempty"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string `xml:"name,attr"`
	Classname string `xml:"classname,attr"`
	Time      string `xml:"time,attr"`
	// File and Line point at the test in its suite, for the CI reporters
	// that annotate the source (dorny/test-reporter, GitLab).
	File      string        `xml:"file,attr,omitempty"`
	Line      int           `xml:"line,attr,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Error     *junitMessage `xml:"error,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Body    string `xml:",chardata"`
}

// Build renders the summary as JUnit XML.
func (*JUnit) Build(s *Summary) ([]byte, error) {
	root := junitSuites{Name: "tamesu", Time: fmt.Sprintf("%.3f", s.Duration().Seconds())}
	bySuite := map[string]*junitSuite{}
	suiteTime := map[string]float64{}
	var order []string
	for _, t := range s.Tests {
		js, ok := bySuite[t.Suite]
		if !ok {
			js = &junitSuite{Name: t.Suite}
			bySuite[t.Suite] = js
			order = append(order, t.Suite)
		}
		var dur float64
		for _, r := range t.Runs {
			dur += r.Duration.Seconds()
		}
		suiteTime[t.Suite] += dur
		c := junitCase{Name: t.Name, Classname: t.Suite, Time: fmt.Sprintf("%.3f", dur), File: relativeURI(t.File), Line: t.Line}

		var body strings.Builder
		for _, r := range t.Runs {
			if r.Passed {
				continue
			}
			fmt.Fprintf(&body, "run %d/%d:\n", r.Index, t.Repeat)
			if r.Error != "" {
				fmt.Fprintf(&body, "  %s: %s\n", r.ErrorKind, r.Error)
			}
			for _, a := range r.FailedAssertions() {
				fmt.Fprintf(&body, "  %s: %s\n", a.Name, a.Reason)
			}
		}
		var transcript strings.Builder
		for _, r := range t.Runs {
			if r.Passed {
				continue
			}
			fmt.Fprintf(&transcript, "run %d transcript:\n", r.Index)
			for _, turn := range r.Transcript {
				fmt.Fprintf(&transcript, "  %s: %s\n", turn.Role, turn.Text)
			}
			break
		}
		c.SystemOut = transcript.String()

		passed := 0
		for _, r := range t.Runs {
			if r.Passed {
				passed++
			}
		}
		switch {
		case t.Skipped():
			js.Skipped++
			root.Skipped++
			c.Skipped = &junitMessage{Message: t.SkipReason()}
		case t.Quarantined():
			// A quarantined failure is skipped for the CI tool, with the
			// failure kept in the message and the body.
			js.Skipped++
			root.Skipped++
			msg := fmt.Sprintf("quarantined%s: %s", optional(" until ", t.Quarantine.Until), t.Quarantine.Reason)
			c.Skipped = &junitMessage{Message: msg, Body: body.String()}
		case t.Errored():
			js.Errors++
			root.Errors++
			msg := "infrastructure error"
			for _, r := range t.Runs {
				if r.Error != "" {
					msg = r.ErrorKind + ": " + r.Error
					break
				}
			}
			c.Error = &junitMessage{Message: msg, Type: "error", Body: body.String()}
		case !t.Passed:
			js.Failures++
			root.Failures++
			msg := fmt.Sprintf("%d of %d runs passed (threshold %.0f%%)", passed, len(t.Runs), t.PassThreshold*100)
			if t.Repeat == 1 {
				msg = firstLine(strings.TrimPrefix(body.String(), "run 1/1:\n"), "failed")
			}
			c.Failure = &junitMessage{Message: msg, Type: "failure", Body: body.String()}
		}
		js.Tests++
		root.Tests++
		js.Cases = append(js.Cases, c)
	}
	for _, name := range order {
		js := bySuite[name]
		js.Time = fmt.Sprintf("%.3f", suiteTime[name])
		root.Suites = append(root.Suites, *js)
	}
	b, err := xml.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(b, '\n')...), nil
}

// Finish writes junit.xml.
func (j *JUnit) Finish(_ context.Context, s *Summary) error {
	b, err := j.Build(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(j.dir, "junit.xml"), b, 0o644)
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
