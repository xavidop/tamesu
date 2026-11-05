package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/lint"
)

func (a *app) newLintCmd() *cobra.Command {
	var asJSON, strict bool
	cmd := &cobra.Command{
		Use:   "lint [paths...]",
		Short: "Find weak tests: judge-only checks, untagged tests, duplicate goals, limits that never bind",
		Long: "Lint loads tamesu.yaml and the suites like validate does, then looks for what\n" +
			"validation accepts but a reviewer would not: tests with no assertions or only model\n" +
			"verdicts, tests nothing can select, the same goal written twice, turn and latency\n" +
			"limits that can never fail, vague rubrics, personas without a style, and targets,\n" +
			"packs, vars and secrets nothing uses. Each finding names its rule, so a rule can be\n" +
			"looked up in the docs. Exit 0 with findings unless --strict; 2 when a suite does not load.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			p, perrs, err := a.loadProject(cmd, false)
			if err != nil {
				return err
			}
			paths, err := a.suitePaths(args, p)
			if err != nil {
				return err
			}
			files, err := a.loadSuites(paths, p, false)
			if err != nil {
				return err
			}
			nErr, _ := printErrors(cmd.ErrOrStderr(), perrs.Only(), a.style(cmd.ErrOrStderr()))
			var findings []lint.Finding
			suites := 0
			for _, f := range files {
				if e, _ := printErrors(cmd.ErrOrStderr(), f.Errs.Only(), a.style(cmd.ErrOrStderr())); e > 0 || f.Suite == nil {
					nErr += e
					continue
				}
				suites++
				raw, _ := os.ReadFile(f.Path)
				findings = append(findings, lint.Suite(f.Suite, p, raw)...)
			}
			if nErr > 0 {
				return silentExit(ExitConfig)
			}
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if findings == nil {
					findings = []lint.Finding{}
				}
				if err := enc.Encode(findings); err != nil {
					return err
				}
			} else {
				st := a.style(out)
				for _, f := range findings {
					_, _ = fmt.Fprintln(out, f.Format(st.Warn))
				}
				byRule := map[string]int{}
				for _, f := range findings {
					byRule[f.Rule]++
				}
				var parts []string
				for _, r := range lint.Rules {
					if n := byRule[r]; n > 0 {
						parts = append(parts, fmt.Sprintf("%s ×%d", r, n))
					}
				}
				summary := fmt.Sprintf("lint: %s, %s", plural(suites, "suite"), plural(len(findings), "finding"))
				if len(parts) > 0 {
					summary += " (" + strings.Join(parts, ", ") + ")"
				}
				_, _ = fmt.Fprintln(out, summary)
			}
			if strict && len(findings) > 0 {
				return silentExit(ExitFailed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the findings as JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit 1 when there are findings")
	return cmd
}
