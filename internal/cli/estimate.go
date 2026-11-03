package cli

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/report"
)

// estimateLine is one selected test's expected spend.
type estimateLine struct {
	Suite, Name string
	Repeat      int
	// Seen is how many past runs priced the test; PerRun their median cost.
	Seen   int
	PerRun float64
}

// Total is the test's expected spend over its repeats.
func (l estimateLine) Total() float64 { return l.PerRun * float64(l.Repeat) }

// estimateCost prices the selected tests from the history of past runs:
// each test's median llm cost per run over the runs that priced it, times
// its repeat count. Tests the history never saw are listed without a price.
func estimateCost(p config.Project, suites []*config.Suite, filter *regexp.Regexp, tags []string, target string, repeatOverride int, h *report.History) []estimateLine {
	costs := map[string][]float64{}
	if h != nil {
		for _, t := range h.Tests {
			var vals []float64
			for _, pt := range t.Points {
				if pt.CostUSD > 0 {
					vals = append(vals, pt.CostUSD)
				}
			}
			costs[t.Suite+"\x00"+t.Name] = vals
			costs["\x00"+t.Name] = append(costs["\x00"+t.Name], vals...)
		}
	}
	var lines []estimateLine
	for _, s := range suites {
		d := s.EffectiveDefaults(p)
		for _, t := range s.SelectTests(filter, tags, target) {
			repeat := d.Repeat
			if t.Repeat != nil && *t.Repeat > 0 {
				repeat = *t.Repeat
			}
			if repeatOverride > 0 {
				repeat = repeatOverride
			}
			if repeat <= 0 {
				repeat = 1
			}
			vals := costs[s.Suite+"\x00"+t.Name]
			if len(vals) == 0 {
				vals = costs["\x00"+t.Name]
			}
			line := estimateLine{Suite: s.Suite, Name: t.Name, Repeat: repeat, Seen: len(vals)}
			if len(vals) > 0 {
				line.PerRun = median(vals)
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func median(vals []float64) float64 {
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// printEstimate writes the estimate as a table with a total and the budget
// verdict. runs is how many past runs were read.
func printEstimate(w io.Writer, lines []estimateLine, runs int, budget float64) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "suite / test\truns seen\tper run\trepeat\testimate")
	total, unknown := 0.0, 0
	for _, l := range lines {
		per, est := "?", "?"
		if l.Seen > 0 {
			per = fmt.Sprintf("$%.4f", l.PerRun)
			est = fmt.Sprintf("$%.4f", l.Total())
			total += l.Total()
		} else {
			unknown++
		}
		_, _ = fmt.Fprintf(tw, "%s / %s\t%d\t%s\t%d\t%s\n", l.Suite, l.Name, l.Seen, per, l.Repeat, est)
	}
	_ = tw.Flush()
	var parts []string
	parts = append(parts, fmt.Sprintf("estimate: $%.4f for %s from %s", total, plural(len(lines), "test"), plural(runs, "past run")))
	switch {
	case unknown == 1:
		parts = append(parts, "1 test has no history and is not priced")
	case unknown > 1:
		parts = append(parts, fmt.Sprintf("%d tests have no history and are not priced", unknown))
	}
	switch {
	case budget <= 0:
	case total > budget:
		parts = append(parts, fmt.Sprintf("OVER the budget of $%.2f (budgets.max_cost_usd)", budget))
	default:
		parts = append(parts, fmt.Sprintf("within the budget of $%.2f", budget))
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", strings.Join(parts, " · "))
}
