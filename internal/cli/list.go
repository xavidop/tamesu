package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// listItem is one test as `tamesu list --json` prints it.
type listItem struct {
	Suite  string   `json:"suite"`
	File   string   `json:"file"`
	Name   string   `json:"name"`
	Target string   `json:"target"`
	Type   string   `json:"type"`
	Tags   []string `json:"tags"`
}

func (a *app) newListCmd() *cobra.Command {
	var asJSON bool
	var sel selectFlags
	cmd := &cobra.Command{
		Use:   "list [paths...]",
		Short: "List the tests of the suites",
		Long: "List prints one line per test (suite, test, target, target type and tags) for the\n" +
			"suites given, or the project's suites:. The same --filter, --tags, --target and\n" +
			"--shard flags as run select tests.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			filter, err := sel.compile()
			if err != nil {
				return err
			}
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
			shard, err := sel.shardOf()
			if err != nil {
				return usageError(err)
			}
			var items []listItem
			k := 0
			for _, f := range files {
				e, _ := printErrors(cmd.ErrOrStderr(), f.Errs.Only(), a.style(cmd.ErrOrStderr()))
				nErr += e
				if f.Suite == nil || e > 0 {
					continue
				}
				for _, t := range f.Suite.SelectTests(filter, sel.tags, sel.target) {
					keep := shard.Keeps(k)
					k++
					if !keep {
						continue
					}
					target, _ := f.Suite.TargetOf(t)
					items = append(items, listItem{Suite: f.Suite.Suite, File: f.Path, Name: t.Name, Target: t.Target, Type: target.Type, Tags: f.Suite.TagsOf(t)})
				}
			}
			if nErr > 0 {
				return silentExit(ExitConfig)
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if items == nil {
					items = []listItem{}
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(items)
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "SUITE\tTEST\tTARGET\tTYPE\tTAGS")
			for _, it := range items {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.Suite, it.Name, it.Target, it.Type, strings.Join(it.Tags, ","))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON instead of a table")
	sel.add(cmd)
	return cmd
}
