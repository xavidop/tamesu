// Package calibrate measures a judge model against labelled transcripts:
// conversations a person has already marked pass or fail for a rubric. The
// agreement rate says how far the judge can be trusted on that rubric.
package calibrate

import (
	"context"
	"errors"
	"fmt"
	"github.com/xavidop/tamesu/report"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/target"
)

// Turn is one line of a labelled transcript.
type Turn struct {
	Role string `yaml:"role" json:"role"`
	Text string `yaml:"text" json:"text"`
}

// ToolCall is a tool the agent called in the labelled conversation.
type ToolCall struct {
	Name string         `yaml:"name" json:"name"`
	Args map[string]any `yaml:"args,omitempty" json:"args,omitempty"`
}

// Label is one labelled example: a rubric, a transcript and the verdict a
// person gave.
type Label struct {
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	Rubric string `yaml:"rubric" json:"rubric"`
	// Scale is binary (the default) or 1-5 with Min, as in a judge assertion.
	Scale float64Scale `yaml:"scale,omitempty" json:"scale,omitempty"`
	Min   float64      `yaml:"min,omitempty" json:"min,omitempty"`
	// Expected is pass or fail.
	Expected   string     `yaml:"expected" json:"expected"`
	Transcript []Turn     `yaml:"transcript" json:"transcript"`
	ToolCalls  []ToolCall `yaml:"tool_calls,omitempty" json:"tool_calls,omitempty"`
}

// float64Scale reads scale as a string ("1-5") or a number (5).
type float64Scale string

func (s *float64Scale) UnmarshalYAML(n *yaml.Node) error {
	*s = float64Scale(n.Value)
	return nil
}

// Load reads labels from a YAML or JSON file: a list, or an object with a
// labels key.
func Load(path string) ([]Label, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes labels from YAML (JSON is YAML).
func Parse(b []byte) ([]Label, error) {
	var list []Label
	if err := yaml.Unmarshal(b, &list); err != nil {
		var doc struct {
			Labels []Label `yaml:"labels"`
		}
		if err2 := yaml.Unmarshal(b, &doc); err2 != nil || len(doc.Labels) == 0 {
			return nil, fmt.Errorf("labels: want a list of {rubric, expected, transcript} or an object with labels: %w", err)
		}
		list = doc.Labels
	}
	if len(list) == 0 {
		return nil, errors.New("labels: the file has no labels")
	}
	for i := range list {
		l := &list[i]
		if l.Name == "" {
			l.Name = fmt.Sprintf("label %d", i+1)
		}
		if strings.TrimSpace(l.Rubric) == "" {
			return nil, fmt.Errorf("%s: rubric is required", l.Name)
		}
		switch strings.ToLower(strings.TrimSpace(l.Expected)) {
		case "pass", "true", "yes":
			l.Expected = "pass"
		case "fail", "false", "no":
			l.Expected = "fail"
		default:
			return nil, fmt.Errorf("%s: expected must be pass or fail (have %q)", l.Name, l.Expected)
		}
		if len(l.Transcript) == 0 {
			return nil, fmt.Errorf("%s: transcript is required", l.Name)
		}
		for j, t := range l.Transcript {
			switch strings.ToLower(t.Role) {
			case "user", "human", "customer":
				l.Transcript[j].Role = "user"
			case "agent", "assistant", "bot":
				l.Transcript[j].Role = "agent"
			default:
				return nil, fmt.Errorf("%s: transcript[%d].role must be user or agent (have %q)", l.Name, j, t.Role)
			}
		}
	}
	return list, nil
}

// Request builds the judge request for a label, as a judge assertion would.
func (l Label) Request() assert.JudgeRequest {
	req := assert.JudgeRequest{Rubric: l.Rubric, Scale: string(l.Scale), Min: l.Min}
	if req.Scale == "" {
		req.Scale = "binary"
	}
	idx := 0
	for _, t := range l.Transcript {
		turn := assert.Turn{Role: t.Role, Text: t.Text}
		if t.Role == "agent" {
			idx++
			turn.Index = idx
		}
		req.Transcript = append(req.Transcript, turn)
	}
	for _, c := range l.ToolCalls {
		req.ToolCalls = append(req.ToolCalls, target.ToolCall{Name: c.Name, Args: c.Args})
	}
	return req
}

// Result is the judge's verdict on one label.
type Result struct {
	Name     string  `json:"name"`
	Rubric   string  `json:"rubric"`
	Expected string  `json:"expected"`
	Got      string  `json:"got"`
	Agree    bool    `json:"agree"`
	Score    float64 `json:"score,omitempty"`
	Reason   string  `json:"reason,omitempty"`
	Error    string  `json:"error,omitempty"`
}

// RubricStats aggregates the labels of one rubric.
type RubricStats struct {
	Rubric    string  `json:"rubric"`
	Labels    int     `json:"labels"`
	Agree     int     `json:"agree"`
	FalsePass int     `json:"false_pass"`
	FalseFail int     `json:"false_fail"`
	Errors    int     `json:"errors"`
	Agreement float64 `json:"agreement"`
}

// Report is the calibration of a judge against a label set.
type Report struct {
	Labels    int           `json:"labels"`
	Agree     int           `json:"agree"`
	FalsePass int           `json:"false_pass"`
	FalseFail int           `json:"false_fail"`
	Errors    int           `json:"errors"`
	Agreement float64       `json:"agreement"`
	Rubrics   []RubricStats `json:"rubrics"`
	Results   []Result      `json:"results"`
}

// Run judges every label (parallel at a time) and aggregates the agreement.
// A judge error counts as a disagreement.
func Run(ctx context.Context, judge assert.Judger, labels []Label, parallel int) *Report {
	if parallel < 1 {
		parallel = 1
	}
	results := make([]Result, len(labels))
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	for i, l := range labels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := Result{Name: l.Name, Rubric: l.Rubric, Expected: l.Expected}
			v, err := judge.Judge(ctx, l.Request())
			switch {
			case err != nil:
				r.Error = err.Error()
				r.Got = "error"
			case v.Pass:
				r.Got = "pass"
			default:
				r.Got = "fail"
			}
			r.Score, r.Reason = v.Score, v.Reason
			r.Agree = r.Got == r.Expected
			results[i] = r
		}()
	}
	wg.Wait()
	return Summarize(results)
}

// Summarize aggregates results into a report.
func Summarize(results []Result) *Report {
	rep := &Report{Results: results, Labels: len(results)}
	byRubric := map[string]*RubricStats{}
	var order []string
	for _, r := range results {
		st, ok := byRubric[r.Rubric]
		if !ok {
			st = &RubricStats{Rubric: r.Rubric}
			byRubric[r.Rubric] = st
			order = append(order, r.Rubric)
		}
		st.Labels++
		switch {
		case r.Error != "":
			st.Errors++
			rep.Errors++
		case r.Agree:
			st.Agree++
			rep.Agree++
		case r.Got == "pass":
			st.FalsePass++
			rep.FalsePass++
		default:
			st.FalseFail++
			rep.FalseFail++
		}
	}
	for _, k := range order {
		st := byRubric[k]
		st.Agreement = float64(st.Agree) / float64(st.Labels)
		rep.Rubrics = append(rep.Rubrics, *st)
	}
	sort.SliceStable(rep.Rubrics, func(i, j int) bool { return rep.Rubrics[i].Agreement < rep.Rubrics[j].Agreement })
	if rep.Labels > 0 {
		rep.Agreement = float64(rep.Agree) / float64(rep.Labels)
	}
	return rep
}

// Print writes the report for people: a table per rubric, the totals and
// every disagreement with the judge's reason.
func (r *Report) Print(w io.Writer, minAgreement float64) {
	r.PrintStyled(w, minAgreement, report.Style{})
}

// PrintStyled is Print with colours: the verdict green or red, every
// disagreement marked in red.
func (r *Report) PrintStyled(w io.Writer, minAgreement float64, style report.Style) {
	tw := style.NewTable(w, "rubric", "labels", "agree", "false pass", "false fail", "errors", "agreement")
	for _, st := range r.Rubrics {
		agreement := fmt.Sprintf("%.0f%%", st.Agreement*100)
		if st.Agreement < minAgreement {
			agreement = style.Warn(agreement)
		}
		tw.Row(short(st.Rubric, 60), fmt.Sprint(st.Labels), fmt.Sprint(st.Agree), fmt.Sprint(st.FalsePass), fmt.Sprint(st.FalseFail), fmt.Sprint(st.Errors), agreement)
	}
	_ = tw.Flush()
	verdict := style.Pass("OK")
	if r.Agreement < minAgreement {
		verdict = style.Fail("BELOW THE MINIMUM")
	}
	_, _ = fmt.Fprintf(w, "\nagreement %.0f%% (%d of %d labels), %d false pass, %d false fail, %d errors · minimum %.0f%%: %s\n",
		r.Agreement*100, r.Agree, r.Labels, r.FalsePass, r.FalseFail, r.Errors, minAgreement*100, verdict)
	first := true
	for _, res := range r.Results {
		if res.Agree {
			continue
		}
		if first {
			_, _ = fmt.Fprintln(w, "\ndisagreements:")
			first = false
		}
		why := res.Reason
		if res.Error != "" {
			why = "judge error: " + res.Error
		}
		_, _ = fmt.Fprintf(w, "  %s %s: expected %s, judge said %s · %s\n", style.Fail("x"), res.Name, res.Expected, res.Got, short(why, 200))
	}
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
