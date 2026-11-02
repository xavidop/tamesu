package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/report"
)

// DefaultRunsDir is where tamesu run writes when --out is not given, and
// where the MCP resources look for runs.
const DefaultRunsDir = ".tamesu/runs"

// The resource URIs.
const (
	RunsURI            = "tamesu://runs"
	RunFileTemplate    = "tamesu://runs/{run}/{file}"
	RunTestURITemplate = "tamesu://runs/{run}/tests/{test}/{attempt}/{file}"
)

var (
	runFiles  = map[string]string{"summary.json": "application/json", "report.html": "text/html", "junit.xml": "application/xml"}
	testFiles = map[string]string{"result.json": "application/json", "transcript.json": "application/json", "steps.json": "application/json", "console.log": "text/plain", "trace.har": "application/json"}
	// segmentRe is what a run, test or attempt directory name may look like:
	// one path element, no traversal.
	segmentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// runEntry is one line of the tamesu://runs listing.
type runEntry struct {
	Run       string `json:"run"`
	URI       string `json:"uri"`
	StartedAt string `json:"started_at,omitempty"`
	Passed    int    `json:"passed"`
	Failed    int    `json:"failed"`
	Errors    int    `json:"errors"`
	// Quarantined failures count in none of the three above.
	Quarantined int `json:"quarantined,omitempty"`
	Tests       int `json:"tests"`
}

// DefineMCPResources registers the run-directory resources on g: the list of
// runs under runsDir, each run's summary, report and JUnit file, and each
// test run's result, transcript, steps, console log and HAR. Every path is
// confined to runsDir.
func DefineMCPResources(g *genkit.Genkit, runsDir string) {
	if runsDir == "" {
		runsDir = DefaultRunsDir
	}
	genkit.DefineResource(g, "tamesu_runs", &ai.ResourceOptions{
		URI:         RunsURI,
		Description: "The tamesu run directories, newest first, with each run's counts and the URI of its summary",
	}, func(context.Context, *ai.ResourceInput) (*ai.ResourceOutput, error) {
		entries, err := listRuns(runsDir)
		if err != nil {
			return nil, err
		}
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return nil, err
		}
		return textOutput(string(b)), nil
	})
	genkit.DefineResource(g, "tamesu_run_file", &ai.ResourceOptions{
		Template:    RunFileTemplate,
		Description: "A run's summary.json (every test and run with assertions, metrics and cost), report.html or junit.xml",
	}, func(_ context.Context, in *ai.ResourceInput) (*ai.ResourceOutput, error) {
		run, file := in.Variables["run"], in.Variables["file"]
		if _, ok := runFiles[file]; !ok {
			return nil, fmt.Errorf("%s: a run has %s", in.URI, keys(runFiles))
		}
		return readConfined(runsDir, in.URI, run, file)
	})
	genkit.DefineResource(g, "tamesu_run_test_file", &ai.ResourceOptions{
		Template:    RunTestURITemplate,
		Description: "One test run's result.json, transcript.json, steps.json (browser), console.log or trace.har; attempt is run-1, run-2...",
	}, func(_ context.Context, in *ai.ResourceInput) (*ai.ResourceOutput, error) {
		run, test, attempt, file := in.Variables["run"], in.Variables["test"], in.Variables["attempt"], in.Variables["file"]
		if _, ok := testFiles[file]; !ok {
			return nil, fmt.Errorf("%s: a test run has %s", in.URI, keys(testFiles))
		}
		return readConfined(runsDir, in.URI, run, "tests", test, attempt, file)
	})
}

// listRuns reads the run directories under runsDir, newest first.
func listRuns(runsDir string) ([]runEntry, error) {
	entries, err := os.ReadDir(runsDir)
	if os.IsNotExist(err) {
		return []runEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []runEntry{}
	for _, e := range entries {
		if !e.IsDir() || !segmentRe.MatchString(e.Name()) {
			continue
		}
		entry := runEntry{Run: e.Name(), URI: strings.Replace(RunFileTemplate, "{run}", e.Name(), 1)}
		entry.URI = strings.Replace(entry.URI, "{file}", "summary.json", 1)
		if s, err := report.LoadSummary(filepath.Join(runsDir, e.Name())); err == nil {
			entry.Passed, entry.Failed, entry.Errors = s.Counts()
			entry.Quarantined = len(s.Quarantined())
			entry.Tests = len(s.Tests)
			if !s.StartedAt.IsZero() {
				entry.StartedAt = s.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Run > out[j].Run })
	return out, nil
}

// readConfined reads runsDir/segments..., refusing any segment that is not
// a plain directory or file name.
func readConfined(runsDir, uri string, segments ...string) (*ai.ResourceOutput, error) {
	for _, seg := range segments {
		if !segmentRe.MatchString(seg) {
			return nil, fmt.Errorf("%s: invalid path element %q", uri, seg)
		}
	}
	path := filepath.Join(append([]string{runsDir}, segments...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	return textOutput(string(b)), nil
}

func textOutput(text string) *ai.ResourceOutput {
	return &ai.ResourceOutput{Content: []*ai.Part{ai.NewTextPart(text)}}
}

func keys(m map[string]string) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
