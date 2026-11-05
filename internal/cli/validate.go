package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) newValidateCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "validate [paths...]",
		Short: "Check tamesu.yaml and the suites without running them",
		Long: "Validate loads tamesu.yaml and every suite (the paths and globs given, or the\n" +
			"project's suites:) and prints each problem as file:line: message. Unset ${VAR}\n" +
			"values are warnings here, so suites can be checked on a machine without them.\n" +
			"Exit code 2 when there is an error (or a warning with --strict).",
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
			st := a.style(out)
			nErr, nWarn := printErrors(out, perrs, st)
			nTests := 0
			for _, f := range files {
				e, w := printErrors(out, f.Errs, st)
				nErr += e
				nWarn += w
				if f.Suite != nil {
					nTests += len(f.Suite.Tests)
				}
			}
			errWord, warnWord := plural(nErr, "error"), plural(nWarn, "warning")
			if nErr > 0 {
				errWord = st.Fail(errWord)
			}
			if nWarn > 0 {
				warnWord = st.Warn(warnWord)
			}
			_, _ = fmt.Fprintf(out, "%s, %s, %s, %s\n", plural(len(files), "suite"), plural(nTests, "test"), errWord, warnWord)
			if nErr > 0 || (strict && nWarn > 0) {
				return silentExit(ExitConfig)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors")
	return cmd
}
