package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
)

func (a *app) newPlaybooksCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "playbooks [name]",
		Short: "The red-team playbooks: attack families a suite's red_team block can run",
		Long: "Playbooks lists the built-in red-team playbooks: for each, the OWASP Top 10 for LLM\n" +
			"Applications category it covers, how many attacks it holds and what it tests. With a\n" +
			"name it prints that playbook's attacks: the attacker's persona and tactics, the goal and\n" +
			"the assertions that grade the agent. A suite runs them with a red_team block.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			if len(args) == 1 {
				p, ok := config.BuiltinPlaybook(args[0])
				if !ok {
					return configError(fmt.Errorf("unknown playbook %q (have %s)", args[0], strings.Join(config.PlaybookNames(), ", ")))
				}
				if asJSON {
					enc := json.NewEncoder(w)
					enc.SetIndent("", "  ")
					return enc.Encode(p)
				}
				printPlaybook(cmd, p)
				return nil
			}
			playbooks := config.BuiltinPlaybooks()
			if asJSON {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(playbooks)
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "PLAYBOOK\tOWASP\tATTACKS\tTESTS")
			for _, p := range playbooks {
				_, _ = fmt.Fprintf(tw, "%s\t%s %s\t%d\t%s\n", p.Name, p.OWASP, config.OWASPTitles[p.OWASP], len(p.Attacks), p.Description)
			}
			_ = tw.Flush()
			_, _ = fmt.Fprintf(w, "\n%s; `tamesu playbooks <name>` shows the attacks. A suite runs them with:\n\n  red_team:\n    target: <target name>\n    agent: <what the agent is and what it must never do>\n    playbooks: [%s]\n", plural(len(playbooks), "playbook"), strings.Join(config.PlaybookNames(), ", "))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the playbooks as JSON")
	return cmd
}

func printPlaybook(cmd *cobra.Command, p config.Playbook) {
	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "%s: %s (%s %s)\n%s\n", p.Name, p.Title, p.OWASP, config.OWASPTitles[p.OWASP], p.Description)
	for _, a := range p.Attacks {
		_, _ = fmt.Fprintf(w, "\n%s/%s\n", p.Name, a.Name)
		_, _ = fmt.Fprintf(w, "  goal:     %s\n", strings.Join(strings.Fields(a.Goal), " "))
		if a.Persona.Name != "" || a.Persona.Style != "" {
			_, _ = fmt.Fprintf(w, "  attacker: %s, %s\n", a.Persona.Name, a.Persona.Style)
		}
		if len(a.Tactics) > 0 {
			_, _ = fmt.Fprintln(w, "  tactics:")
			for _, t := range a.Tactics {
				_, _ = fmt.Fprintf(w, "    - %s\n", t)
			}
		}
		var asserts []string
		for _, as := range a.Assert {
			asserts = append(asserts, as.Kind())
		}
		if len(asserts) > 0 {
			_, _ = fmt.Fprintf(w, "  asserts:  %s\n", strings.Join(asserts, ", "))
		}
	}
}
