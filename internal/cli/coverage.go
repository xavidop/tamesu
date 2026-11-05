package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/report"
)

func (a *app) newCoverageCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "coverage <run-dir>",
		Short: "What a run exercised: targets, tags, tools, assertion kinds, stop reasons",
		Long: "Coverage reads a run directory's summary.json and reports what the tests reached:\n" +
			"the targets and tags, every tool the agent called (how often and in how many tests),\n" +
			"the assertion kinds used, how conversations ended, and the tactics of adversarial\n" +
			"personas. When the suites can be loaded, the tools they declare (a model target's\n" +
			"tools, the tools named by tool_called assertions) that no test made the agent call\n" +
			"are listed too. tamesu run writes the same report as coverage.md and coverage.json.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := report.LoadSummary(args[0])
			if err != nil {
				return configError(fmt.Errorf("run directory %s: %w", args[0], err))
			}
			var declared map[string][]string
			if p, _, err := a.loadProject(cmd, false); err == nil {
				var paths []string
				for _, t := range s.Tests {
					if t.File != "" && !slices.Contains(paths, t.File) {
						paths = append(paths, t.File)
					}
				}
				if files, err := a.loadSuites(paths, p, false); err == nil {
					var suites []*config.Suite
					for _, f := range files {
						if f.Suite != nil {
							suites = append(suites, f.Suite)
						}
					}
					declared = declaredTools(suites)
				}
			}
			c := report.BuildCoverage(s, declared)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(c)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), c.ConsoleStyled(a.style(cmd.OutOrStdout())))
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the coverage as JSON")
	return cmd
}

// declaredTools lists, per target, the tools the suites know about: a model
// target's tools and the tools named by tool_called and tool_sequence
// assertions of its tests.
func declaredTools(suites []*config.Suite) map[string][]string {
	out := map[string]map[string]bool{}
	add := func(target, tool string) {
		if tool == "" {
			return
		}
		if out[target] == nil {
			out[target] = map[string]bool{}
		}
		out[target][tool] = true
	}
	for _, s := range suites {
		for name, t := range s.Targets {
			for _, tool := range t.Tools {
				add(name, tool.Name)
			}
		}
		for _, t := range s.Tests {
			for _, a := range t.Assert {
				if a.ToolCalled != nil {
					add(t.Target, a.ToolCalled.Name)
				}
				if a.ToolSequence != nil {
					for _, c := range a.ToolSequence.Calls {
						add(t.Target, c)
					}
				}
			}
		}
	}
	result := make(map[string][]string, len(out))
	for target, tools := range out {
		for tool := range tools {
			result[target] = append(result[target], tool)
		}
		sort.Strings(result[target])
	}
	return result
}
