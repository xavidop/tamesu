package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// RecordRequest is what `tamesu record browser` hands the browser module.
type RecordRequest struct {
	URL string
	// Log receives each step as it is recorded (one line on stderr).
	Log func(step map[string]any)
}

// RecordResult is the recorded steps and their YAML rendering.
type RecordResult struct {
	Steps []map[string]any
	YAML  string
}

func (a *app) newRecordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record a session into deterministic steps",
		Long:  "Record opens the target for you to drive and writes what you did as steps.",
	}
	cmd.AddCommand(a.newRecordBrowserCmd())
	return cmd
}

func (a *app) newRecordBrowserCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "browser <url>",
		Short: "Open a browser on the URL and record your clicks, typing and navigation as action steps",
		Long: "Record browser opens Chromium (with a window) on the URL. Everything you do, clicks,\n" +
			"typing (reported when you leave the field or press Enter), selections, Enter and the\n" +
			"pages you navigate to by hand, becomes a deterministic `action:` step with a durable\n" +
			"selector (an id, a name, an aria-label, else a short path). Passwords are recorded\n" +
			"as {{secrets.PASSWORD}}, never their value. Close the browser (or press Ctrl-C) to\n" +
			"finish: the steps: block is printed, or written with --out, ready to paste into a\n" +
			"test.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.deps.RecordBrowser == nil {
				return configError(errors.New("record browser is not available in this build"))
			}
			res, err := a.deps.RecordBrowser(cmd.Context(), RecordRequest{URL: args[0], Log: func(step map[string]any) {
				for k, v := range step {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "recorded %s: %v\n", k, v)
				}
			}})
			// Ctrl-C ends the session: what was recorded is still the result.
			interrupted := errors.Is(err, context.Canceled) && res.YAML != ""
			if err != nil && !interrupted {
				return infraError(err)
			}
			if out == "" {
				_, _ = fmt.Fprint(cmd.OutOrStdout(), res.YAML)
				return nil
			}
			if err := os.WriteFile(out, []byte(res.YAML), 0o644); err != nil {
				return infraError(err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %d step(s)\n", out, len(res.Steps))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write the steps here (default: print them)")
	return cmd
}
