package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// HistoryRun is one past run in a runs directory.
type HistoryRun struct {
	Dir       string            `json:"dir"`
	StartedAt time.Time         `json:"started_at"`
	Labels    map[string]string `json:"labels,omitempty"`
	Passed    int               `json:"passed"`
	Failed    int               `json:"failed"`
	Errored   int               `json:"errored"`
}

// HistoryPoint is one test's result in one run.
type HistoryPoint struct {
	// Run indexes History.Runs.
	Run int `json:"run"`
	// Status is pass, fail, error, quarantined or skipped.
	Status string `json:"status"`
	// P95 is the median over the run's repeats of turn_latency_ms_p95 (or
	// the first latency metric the test has); 0 when none.
	P95 float64 `json:"p95_ms,omitempty"`
	// CostUSD is the median llm cost of the test's repeats.
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// TestHistory is one test across the runs.
type TestHistory struct {
	Suite  string         `json:"suite"`
	Name   string         `json:"name"`
	Points []HistoryPoint `json:"points"`
	// Runs is how many of the runs had the test.
	Runs int `json:"runs"`
	// PassRate is passes over runs where the test ran (skipped excluded).
	PassRate float64 `json:"pass_rate"`
	// Flips counts the changes between pass and fail across the points: a
	// test that alternates is flaky long before it is quarantined.
	Flips int `json:"flips"`
	// Streak is the length of the run of the latest status: 3 means the last
	// three results were the same.
	Streak int `json:"streak"`
	// P95First and P95Last are the earliest and latest latency points.
	P95First float64 `json:"p95_first_ms,omitempty"`
	P95Last  float64 `json:"p95_last_ms,omitempty"`
}

// Flaky reports whether the test changed verdict twice or more in the window.
func (t TestHistory) Flaky() bool { return t.Flips >= 2 }

// LatestStatus is the status of the newest point, "" without points.
func (t TestHistory) LatestStatus() string {
	if len(t.Points) == 0 {
		return ""
	}
	return t.Points[len(t.Points)-1].Status
}

// History is the last runs of a runs directory, oldest first, and every
// test's results across them.
type History struct {
	Runs  []HistoryRun  `json:"runs"`
	Tests []TestHistory `json:"tests"`
}

// runDirRe matches the run directories tamesu run names by default.
var runDirRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}`)

// LoadHistory reads the summaries under runsDir (every subdirectory holding
// a summary.json), keeps the last n by start time (all when n <= 0) and
// leaves out the directory named by exclude (the run being reported, which
// the caller appends itself). A directory with no readable summary is
// skipped.
func LoadHistory(runsDir string, n int, exclude string) (*History, error) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, err
	}
	abs := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	excl := ""
	if exclude != "" {
		excl = abs(exclude)
	}
	type loaded struct {
		dir string
		s   *Summary
	}
	var runs []loaded
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(runsDir, e.Name())
		if excl != "" && abs(dir) == excl {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
		if err != nil {
			continue
		}
		var s Summary
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		runs = append(runs, loaded{dir: dir, s: &s})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].s.StartedAt.Equal(runs[j].s.StartedAt) {
			return runs[i].dir < runs[j].dir
		}
		return runs[i].s.StartedAt.Before(runs[j].s.StartedAt)
	})
	if n > 0 && len(runs) > n {
		runs = runs[len(runs)-n:]
	}
	h := &History{}
	for _, r := range runs {
		h.Append(r.dir, r.s)
	}
	return h, nil
}

// Append adds a run as the newest point of the history.
func (h *History) Append(dir string, s *Summary) {
	passed, failed, errored := s.Counts()
	h.Runs = append(h.Runs, HistoryRun{Dir: dir, StartedAt: s.StartedAt, Labels: s.Labels, Passed: passed, Failed: failed, Errored: errored})
	idx := len(h.Runs) - 1
	byKey := map[string]int{}
	for i, t := range h.Tests {
		byKey[t.Suite+" / "+t.Name] = i
	}
	for _, t := range s.Tests {
		key := t.Suite + " / " + t.Name
		i, ok := byKey[key]
		if !ok {
			h.Tests = append(h.Tests, TestHistory{Suite: t.Suite, Name: t.Name})
			i = len(h.Tests) - 1
			byKey[key] = i
		}
		p := HistoryPoint{Run: idx, Status: statusOf(t)}
		m := medianMetrics(t)
		for _, name := range latencyMetrics {
			if v, ok := m[name]; ok {
				p.P95 = v
				break
			}
		}
		p.CostUSD = m["llm_cost_usd"]
		h.Tests[i].Points = append(h.Tests[i].Points, p)
	}
	for i := range h.Tests {
		h.Tests[i].summarise()
	}
	sort.SliceStable(h.Tests, func(a, b int) bool {
		if h.Tests[a].Suite != h.Tests[b].Suite {
			return h.Tests[a].Suite < h.Tests[b].Suite
		}
		return h.Tests[a].Name < h.Tests[b].Name
	})
}

func statusOf(t *TestOutcome) string {
	switch {
	case t.Skipped():
		return "skipped"
	case t.Quarantined():
		return "quarantined"
	case t.Errored():
		return "error"
	case t.Passed:
		return "pass"
	default:
		return "fail"
	}
}

func (t *TestHistory) summarise() {
	ran, passed, flips := 0, 0, 0
	last := ""
	t.P95First, t.P95Last = 0, 0
	for _, p := range t.Points {
		if p.Status == "skipped" {
			continue
		}
		ran++
		verdict := "fail"
		if p.Status == "pass" {
			passed++
			verdict = "pass"
		}
		if last != "" && verdict != last {
			flips++
		}
		last = verdict
		if p.P95 > 0 {
			if t.P95First == 0 {
				t.P95First = p.P95
			}
			t.P95Last = p.P95
		}
	}
	t.Runs = ran
	t.PassRate = 0
	if ran > 0 {
		t.PassRate = float64(passed) / float64(ran)
	}
	t.Flips = flips
	t.Streak = 0
	for i := len(t.Points) - 1; i >= 0; i-- {
		if t.Points[i].Status != t.LatestStatus() {
			break
		}
		t.Streak++
	}
}

// Dots renders the points as one character each, oldest first: ✓ pass,
// ✗ fail, ! error, ~ quarantined, · skipped.
func (t TestHistory) Dots() string {
	var b strings.Builder
	for _, p := range t.Points {
		switch p.Status {
		case "pass":
			b.WriteString("✓")
		case "fail":
			b.WriteString("✗")
		case "error":
			b.WriteString("!")
		case "quarantined":
			b.WriteString("~")
		default:
			b.WriteString("·")
		}
	}
	return b.String()
}

// IsRunsDir reports whether dir looks like a default run directory
// (.tamesu/runs/<timestamp>), so its parent holds the history.
func IsRunsDir(dir string) bool { return runDirRe.MatchString(filepath.Base(dir)) }
