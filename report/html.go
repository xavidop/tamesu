package report

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed html/report.html
var htmlTemplate string

// HTMLFile is the report's file name in the run directory.
const HTMLFile = "report.html"

// HTML writes report.html into the run directory when the run finishes: one
// self-contained page with the transcripts, assertions, metrics, events, the
// screenshots and an audio player, linking the artifacts relative to itself.
type HTML struct{ dir string }

// NewHTML returns the HTML reporter for the run directory.
func NewHTML(dir string) *HTML { return &HTML{dir: dir} }

func (*HTML) RunStarted(RunInfo)                    {}
func (*HTML) TestStarted(*TestOutcome)              {}
func (*HTML) RunFinished(*TestOutcome, *RunOutcome) {}
func (*HTML) TestFinished(*TestOutcome)             {}

// Finish writes report.html.
func (h *HTML) Finish(_ context.Context, s *Summary) error {
	return WriteHTML(h.dir, s)
}

// WriteHTML renders the summary into dir/report.html.
func WriteHTML(dir string, s *Summary) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := RenderHTML(&buf, s, dir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, HTMLFile), buf.Bytes(), 0o644)
}

// LoadSummary reads summary.json from a run directory.
func LoadSummary(dir string) (*Summary, error) {
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s has no summary.json: is it a tamesu run directory?", dir)
		}
		return nil, err
	}
	var s Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s/summary.json: %w", dir, err)
	}
	if s.OutDir == "" {
		s.OutDir = dir
	}
	return &s, nil
}

// View types for the template.
type htmlData struct {
	Summary                 *Summary
	Passed, Failed, Errored int
	Tests                   []htmlTest
}

type htmlTest struct {
	T          *TestOutcome
	Status     string
	Passed     bool
	PassedRuns int
	Duration   time.Duration
	Runs       []htmlRun
}

type htmlRun struct {
	ID               string
	T                *TestOutcome
	R                *RunOutcome
	Audio            string
	Recording        string // the session recording, a WebM or an animated GIF
	RecordingIsVideo bool
	Markers          []htmlMarker
	Screenshots      []htmlLink
	Files            []htmlLink
	Metrics          []htmlMetric
	Events           []htmlEvent
}

type htmlMarker struct {
	Label   string
	Class   string
	Seconds float64
}

type htmlLink struct{ Name, Label, Href string }

type htmlMetric struct {
	Name  string
	Value string
}

type htmlEvent struct {
	Offset string
	Kind   string
	Detail string
}

var htmlFuncs = template.FuncMap{
	"fmtDur":  fmtDuration,
	"fmtTime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"join":    strings.Join,
	"mul":     func(a float64, b int) float64 { return a * float64(b) },
	"json": func(v any) string {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err.Error()
		}
		return string(b)
	},
}

// RenderHTML writes the report for s to w; links are made relative to dir.
func RenderHTML(w io.Writer, s *Summary, dir string) error {
	tmpl, err := template.New("report").Funcs(htmlFuncs).Parse(htmlTemplate)
	if err != nil {
		return err
	}
	data := htmlData{Summary: s}
	data.Passed, data.Failed, data.Errored = s.Counts()
	for ti, t := range s.Tests {
		ht := htmlTest{T: t, Passed: t.Passed, Status: "pass"}
		switch {
		case t.Errored():
			ht.Status = "error"
		case !t.Passed:
			ht.Status = "fail"
		}
		for ri, r := range t.Runs {
			if r.Passed {
				ht.PassedRuns++
			}
			ht.Duration += r.Duration
			ht.Runs = append(ht.Runs, htmlRunView(fmt.Sprintf("t%d-r%d", ti, ri), t, r, dir))
		}
		data.Tests = append(data.Tests, ht)
	}
	return tmpl.Execute(w, data)
}

func htmlRunView(id string, t *TestOutcome, r *RunOutcome, dir string) htmlRun {
	v := htmlRun{ID: id, T: t, R: r}
	base := r.Dir
	if rel, err := filepath.Rel(dir, r.Dir); err == nil && !strings.HasPrefix(rel, "..") {
		base = rel
	} else if r.Dir == "" {
		base = "."
	}
	href := func(name string) string { return filepath.ToSlash(filepath.Join(base, name)) }
	names := make([]string, 0, len(r.Artifacts))
	for name := range r.Artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := r.Artifacts[name]
		link := htmlLink{Name: name, Label: name, Href: href(path)}
		switch {
		case strings.HasSuffix(name, ".wav"):
			v.Audio = link.Href
		case strings.HasSuffix(name, ".webm"), strings.HasSuffix(name, ".gif"):
			v.Recording, v.RecordingIsVideo = link.Href, strings.HasSuffix(name, ".webm")
			continue
		case strings.HasSuffix(name, ".png"):
			link.Label = strings.TrimSuffix(filepath.Base(name), ".png")
			v.Screenshots = append(v.Screenshots, link)
			continue
		}
		v.Files = append(v.Files, link)
	}
	// Step screenshots in step order, with the step as caption.
	if len(r.Steps) > 0 {
		var shots []htmlLink
		for _, st := range r.Steps {
			if st.Screenshot != "" {
				label := fmt.Sprintf("%d %s", st.Index, st.Kind)
				if st.Text != "" {
					label += ": " + short(st.Text, 60)
				}
				shots = append(shots, htmlLink{Name: st.Screenshot, Label: label, Href: href(st.Screenshot)})
			}
		}
		if len(shots) > 0 {
			v.Screenshots = shots
		}
	}
	keys := make([]string, 0, len(r.Metrics))
	for k := range r.Metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v.Metrics = append(v.Metrics, htmlMetric{Name: k, Value: fmtMetric(r.Metrics[k])})
	}
	var start time.Time
	for _, e := range r.Events {
		if start.IsZero() || e.At.Before(start) {
			start = e.At
		}
	}
	for _, e := range r.Events {
		off := e.At.Sub(start)
		v.Events = append(v.Events, htmlEvent{Offset: fmtOffset(off), Kind: e.Kind, Detail: e.Detail})
		if v.Audio != "" {
			switch e.Kind {
			case "user_speech_start":
				v.Markers = append(v.Markers, htmlMarker{Label: "caller", Class: "user", Seconds: off.Seconds()})
			case "agent_speech_start":
				v.Markers = append(v.Markers, htmlMarker{Label: "agent", Class: "agent", Seconds: off.Seconds()})
			case "interruption", "dtmf_sent", "dtmf_received":
				v.Markers = append(v.Markers, htmlMarker{Label: e.Kind, Class: "event", Seconds: off.Seconds()})
			}
		}
	}
	return v
}

func fmtMetric(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}

func fmtOffset(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n-3] + "..."
	}
	return s
}
