package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/report"
)

// matrixAssign is one key set to one value in a matrix variant.
type matrixAssign struct {
	Key, Value string
}

// matrixVariant is one combination of the --matrix keys; nil means no matrix.
type matrixVariant []matrixAssign

// matrixKeyRe is what --matrix accepts on the left of the equals sign.
var matrixKeyRe = regexp.MustCompile(`^(env|models\.(default|judge|simulator|browser_agent|scenario_generator|embedder)|speech\.(tts|stt)\.model|vars\.[A-Za-z_][A-Za-z0-9_]*|targets\.[A-Za-z0-9_-]+\.(model|system|system_file|url|mode|streaming))$`)

// parseMatrixFlags turns --matrix KEY=v1,v2 flags into every combination of
// their values (the cartesian product, in flag order). No flags is no matrix.
func parseMatrixFlags(flags []string) ([]matrixVariant, error) {
	var keys []string
	values := map[string][]string{}
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("--matrix %q: want KEY=value1,value2", f)
		}
		if !matrixKeyRe.MatchString(k) {
			return nil, fmt.Errorf("--matrix %q: the key is one of env, models.<job>, speech.tts.model, speech.stt.model, vars.<name> or targets.<name>.<model|system|system_file|url|mode|streaming>", k)
		}
		var vals []string
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				vals = append(vals, part)
			}
		}
		if _, seen := values[k]; !seen {
			keys = append(keys, k)
		}
		values[k] = append(values[k], vals...)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	variants := []matrixVariant{nil}
	for _, k := range keys {
		var next []matrixVariant
		for _, base := range variants {
			for _, v := range values[k] {
				variant := append(append(matrixVariant(nil), base...), matrixAssign{Key: k, Value: v})
				next = append(next, variant)
			}
		}
		variants = next
	}
	return variants, nil
}

// String words a variant for the console: "models.judge=a, vars.prompt=v2".
func (v matrixVariant) String() string {
	parts := make([]string, 0, len(v))
	for _, a := range v {
		parts = append(parts, a.Key+"="+a.Value)
	}
	return strings.Join(parts, ", ")
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Slug is the variant's directory name: the last part of each key and its
// value, safe for a path ("judge-googleai-gemini-2.5-flash+prompt-v2.md").
func (v matrixVariant) Slug() string {
	parts := make([]string, 0, len(v))
	for _, a := range v {
		key := a.Key[strings.LastIndex(a.Key, ".")+1:]
		val := strings.Trim(slugRe.ReplaceAllString(a.Value, "-"), "-")
		parts = append(parts, key+"-"+val)
	}
	return strings.Join(parts, "+")
}

// Labels are the variant's assignments as run labels.
func (v matrixVariant) Labels() map[string]string {
	out := make(map[string]string, len(v))
	for _, a := range v {
		out[a.Key] = a.Value
	}
	return out
}

// applyMatrix writes a variant's values into the loaded project and suites:
// an environment, a model for a job (on the project and on every suite that
// overrides models), a speech model, a suite var, or a field of a named
// target in every suite that declares it.
func applyMatrix(p *config.Project, suites []*config.Suite, v matrixVariant) error {
	for _, a := range v {
		parts := strings.Split(a.Key, ".")
		switch parts[0] {
		case "env":
			if err := p.ApplyEnvironment(a.Value); err != nil {
				return fmt.Errorf("--matrix env: %w", err)
			}
		case "models":
			set := func(m *config.Models) {
				switch parts[1] {
				case "default":
					m.Default = a.Value
				case "judge":
					m.Judge = a.Value
				case "simulator":
					m.Simulator = a.Value
				case "browser_agent":
					m.BrowserAgent = a.Value
				case "scenario_generator":
					m.ScenarioGenerator = a.Value
				case "embedder":
					m.Embedder = a.Value
				}
			}
			set(&p.Models)
			for _, s := range suites {
				if s.Models != nil {
					set(s.Models)
				}
			}
		case "speech":
			if parts[1] == "tts" {
				p.Speech.TTS.Model = a.Value
			} else {
				p.Speech.STT.Model = a.Value
			}
		case "vars":
			for _, s := range suites {
				if s.Vars == nil {
					s.Vars = map[string]string{}
				}
				s.Vars[parts[1]] = a.Value
			}
		case "targets":
			name, field := parts[1], parts[2]
			found := false
			for _, s := range suites {
				t, ok := s.Targets[name]
				if !ok {
					continue
				}
				found = true
				switch field {
				case "model":
					t.Model = a.Value
				case "system":
					t.System, t.SystemFile = a.Value, ""
				case "system_file":
					t.SystemFile, t.System = a.Value, ""
				case "url":
					t.URL = a.Value
				case "mode":
					t.Mode = a.Value
				case "streaming":
					t.Streaming = a.Value
				}
				s.Targets[name] = t
			}
			if !found {
				return fmt.Errorf("--matrix %s: no selected suite declares a target named %q", a.Key, name)
			}
		}
	}
	return nil
}

// matrixRow is one variant's result in the comparison.
type matrixRow struct {
	Variant  map[string]string `json:"variant"`
	Dir      string            `json:"dir"`
	Passed   int               `json:"passed"`
	Failed   int               `json:"failed"`
	Errored  int               `json:"errored"`
	Tests    int               `json:"tests"`
	Calls    int               `json:"model_calls"`
	CostUSD  float64           `json:"cost_usd"`
	Duration string            `json:"duration"`
	ExitCode int               `json:"exit_code"`
	label    string
}

// runMatrix runs every variant into its own directory under out, prints a
// comparison, writes matrix.md and matrix.json next to the variant
// directories, and returns the worst exit code.
func (a *app) runMatrix(cmd *cobra.Command, out string, variants []matrixVariant, runOnce func(outDir string, v matrixVariant) (*report.Summary, int, error)) error {
	w := cmd.OutOrStdout()
	var rows []matrixRow
	worst := ExitOK
	for i, v := range variants {
		dir := filepath.Join(out, v.Slug())
		_, _ = fmt.Fprintf(w, "==> matrix %d/%d: %s\n", i+1, len(variants), v)
		s, code, err := runOnce(dir, v)
		if err != nil {
			return err
		}
		row := matrixRow{Variant: v.Labels(), Dir: dir, ExitCode: code, label: v.String()}
		if s != nil {
			row.Passed, row.Failed, row.Errored = s.Counts()
			row.Tests = len(s.Tests)
			row.Calls, row.CostUSD = s.Usage.Calls, s.Usage.CostUSD
			row.Duration = report.FormatDuration(s.Duration())
		}
		rows = append(rows, row)
		worst = max(worst, code)
		_, _ = fmt.Fprintln(w)
	}
	_, _ = fmt.Fprintln(w, "matrix:")
	printMatrixTable(w, rows)
	if err := os.MkdirAll(out, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(out, "matrix.md"), []byte(matrixMarkdown(rows)), 0o644)
		if b, err := json.MarshalIndent(rows, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(out, "matrix.json"), append(b, '\n'), 0o644)
		}
	}
	_, _ = fmt.Fprintf(w, "\nmatrix: %s in %s (matrix.md, matrix.json)\n", plural(len(rows), "variant"), out)
	if worst != ExitOK {
		return silentExit(worst)
	}
	return nil
}

func printMatrixTable(w io.Writer, rows []matrixRow) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "variant\tpassed\tfailed\terrors\tmodel calls\tcost\tduration\texit")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t$%.4f\t%s\t%d\n", r.label, r.Passed, r.Failed, r.Errored, r.Calls, r.CostUSD, r.Duration, r.ExitCode)
	}
	_ = tw.Flush()
}

func matrixMarkdown(rows []matrixRow) string {
	var b strings.Builder
	b.WriteString("# Matrix\n\n| Variant | Passed | Failed | Errors | Model calls | Cost | Duration | Exit |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | $%.4f | %s | %d |\n", r.label, r.Passed, r.Failed, r.Errored, r.Calls, r.CostUSD, r.Duration, r.ExitCode)
	}
	b.WriteString("\nEach variant's run directory, with its own reports, sits next to this file.\n")
	return b.String()
}
